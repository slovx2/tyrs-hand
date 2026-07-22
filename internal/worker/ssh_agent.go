package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type sshCapabilityStatus struct {
	Status          string `json:"status"`
	Revision        string `json:"revision,omitempty"`
	CredentialCount int    `json:"credentialCount"`
	HostCount       int    `json:"hostCount"`
	LastError       string `json:"lastError,omitempty"`
}

type managedAgent struct {
	command *exec.Cmd
	socket  string
}

type sshAgentManager struct {
	root   string
	client *workerprotocol.Client
	logger *zap.Logger

	mu      sync.RWMutex
	etag    string
	current *managedAgent
	status  sshCapabilityStatus
}

func newSSHAgentManager(root string, client *workerprotocol.Client,
	logger *zap.Logger,
) *sshAgentManager {
	return &sshAgentManager{root: root, client: client, logger: logger,
		status: sshCapabilityStatus{Status: "starting"}}
}

func (m *sshAgentManager) Run(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Join(m.root, "keys"), 0o755); err != nil {
		return err
	}
	if err := os.Chmod(m.root, 0o755); err != nil {
		return err
	}
	if err := m.sync(ctx); err != nil {
		m.setError(err)
		m.logger.Warn("首次同步 SSH 配置失败，将继续重试", zap.Error(err))
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.Close()
			return ctx.Err()
		case <-ticker.C:
			if err := m.sync(ctx); err != nil {
				m.setError(err)
				m.logger.Warn("同步 SSH 配置失败", zap.Error(err))
			}
		}
	}
}

func (m *sshAgentManager) sync(ctx context.Context) error {
	m.mu.RLock()
	etag := m.etag
	m.mu.RUnlock()
	configuration, nextETag, changed, err := m.client.SSHConfiguration(ctx, etag)
	if err != nil || !changed {
		return err
	}
	next, err := m.startGeneration(ctx, configuration)
	if err != nil {
		return err
	}
	if err := switchSymlink(filepath.Join(m.root, "current.sock"), next.socket); err != nil {
		_ = stopAgent(next)
		return err
	}
	m.mu.Lock()
	previous := m.current
	m.current = next
	m.etag = nextETag
	m.status = sshCapabilityStatus{Status: "ready", Revision: configuration.Revision,
		CredentialCount: len(configuration.Credentials), HostCount: len(configuration.Hosts)}
	m.mu.Unlock()
	if previous != nil {
		go func() {
			timer := time.NewTimer(5 * time.Minute)
			defer timer.Stop()
			select {
			case <-ctx.Done():
			case <-timer.C:
			}
			_ = stopAgent(previous)
		}()
	}
	return nil
}

func (m *sshAgentManager) startGeneration(ctx context.Context,
	configuration workerprotocol.SSHConfiguration,
) (*managedAgent, error) {
	generation := fmt.Sprintf("agent-%d.sock", time.Now().UnixNano())
	socket := filepath.Join(m.root, generation)
	command := exec.Command("ssh-agent", "-D", "-a", socket)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("启动 ssh-agent: %w", err)
	}
	managed := &managedAgent{command: command, socket: socket}
	go func() { _ = command.Wait() }()
	if err := waitForSocket(ctx, socket); err != nil {
		_ = stopAgent(managed)
		return nil, err
	}
	if err := os.Chmod(socket, 0o666); err != nil {
		_ = stopAgent(managed)
		return nil, err
	}
	if err := m.loadKeys(socket, configuration.Credentials); err != nil {
		_ = stopAgent(managed)
		return nil, err
	}
	if err := m.writePublicConfiguration(configuration); err != nil {
		_ = stopAgent(managed)
		return nil, err
	}
	return managed, nil
}

func waitForSocket(ctx context.Context, path string) error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		connection, err := net.DialTimeout("unix", path, 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("等待 ssh-agent Socket 超时")
		case <-ticker.C:
		}
	}
}

func (m *sshAgentManager) loadKeys(socket string,
	credentials []workerprotocol.SSHCredential,
) error {
	connection, err := net.Dial("unix", socket)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	client := agent.NewClient(connection)
	for _, credential := range credentials {
		var privateKey any
		if credential.Passphrase == "" {
			privateKey, err = ssh.ParseRawPrivateKey([]byte(credential.PrivateKey))
		} else {
			privateKey, err = ssh.ParseRawPrivateKeyWithPassphrase(
				[]byte(credential.PrivateKey), []byte(credential.Passphrase))
		}
		if err != nil {
			return fmt.Errorf("解析 SSH 凭证 %s: %w", credential.ID, err)
		}
		if err := client.Add(agent.AddedKey{PrivateKey: privateKey,
			Comment: credential.Fingerprint}); err != nil {
			return fmt.Errorf("加载 SSH 凭证 %s: %w", credential.ID, err)
		}
	}
	return nil
}

func (m *sshAgentManager) writePublicConfiguration(
	configuration workerprotocol.SSHConfiguration,
) error {
	keysRoot := filepath.Join(m.root, "keys")
	for _, credential := range configuration.Credentials {
		path := filepath.Join(keysRoot, credential.ID.String()+".pub")
		if err := atomicWrite(path, []byte(strings.TrimSpace(credential.PublicKey)+"\n"), 0o644); err != nil {
			return err
		}
	}
	var builder strings.Builder
	builder.WriteString("Host *\n  StrictHostKeyChecking accept-new\n")
	for _, host := range configuration.Hosts {
		fmt.Fprintf(&builder, "\nHost %s\n  HostName %s\n  Port %d\n  User %s\n",
			host.Alias, host.Hostname, host.Port, host.Username)
		fmt.Fprintf(&builder, "  IdentityFile %s\n  IdentitiesOnly yes\n",
			filepath.Join(keysRoot, host.CredentialID.String()+".pub"))
		if host.ProxyJumpAlias != "" {
			fmt.Fprintf(&builder, "  ProxyJump %s\n", host.ProxyJumpAlias)
		}
	}
	return atomicWrite(filepath.Join(m.root, "ssh_config"), []byte(builder.String()), 0o644)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(temporary, mode); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func switchSymlink(link, target string) error {
	temporary := link + ".tmp"
	_ = os.Remove(temporary)
	if err := os.Symlink(filepath.Base(target), temporary); err != nil {
		return err
	}
	return os.Rename(temporary, link)
}

func stopAgent(value *managedAgent) error {
	if value == nil || value.command == nil || value.command.Process == nil {
		return nil
	}
	err := value.command.Process.Kill()
	_ = os.Remove(value.socket)
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func (m *sshAgentManager) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := "error"
	if m.current != nil {
		status = "degraded"
	}
	m.status.Status, m.status.LastError = status, err.Error()
}

func (m *sshAgentManager) Status() sshCapabilityStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

func (m *sshAgentManager) Close() {
	m.mu.Lock()
	current := m.current
	m.current = nil
	m.mu.Unlock()
	_ = stopAgent(current)
	_ = os.Remove(filepath.Join(m.root, "current.sock"))
}
