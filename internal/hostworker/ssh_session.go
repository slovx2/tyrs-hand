package hostworker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/creack/pty"
	"github.com/pkg/sftp"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

func (s *SSHServer) serveSFTP(channel ssh.Channel) {
	server, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(s.options.Home))
	if err != nil {
		s.writeExit(channel, 1)
		return
	}
	err = server.Serve()
	_ = server.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		s.writeExit(channel, 1)
		return
	}
	s.writeExit(channel, 0)
}

func (s *SSHServer) runCommand(channel ssh.Channel, state *sshSessionState, command string) {
	trimmed := strings.TrimSpace(command)
	switch trimmed {
	case "codex app-server proxy":
		err := s.options.Runtime.ServeDesktop(&sshChannelConnection{Channel: channel,
			local: s.listener.Addr(), remote: streamAddress("desktop")})
		if err != nil {
			s.options.Logger.Warn("Codex Desktop SSH 会话停止", zap.Error(err))
			s.writeExit(channel, 1)
			return
		}
		s.writeExit(channel, 0)
		return
	case "tyrs-hand-worker browser proxy":
		if s.options.BrowserProxy == nil {
			s.writeExit(channel, 127)
			return
		}
		err := s.options.BrowserProxy(context.Background(), channel)
		if err != nil {
			s.writeExit(channel, 1)
			return
		}
		s.writeExit(channel, 0)
		return
	}

	arguments := []string(nil)
	if trimmed != "" {
		arguments = []string{"-lc", command}
	}
	process := exec.Command(s.options.Shell, arguments...)
	process.Dir = s.options.Home
	values := map[string]string{"HOME": s.options.Home, "CODEX_HOME": s.options.CodexHome}
	for name, value := range state.environment {
		if allowedSSHEnvironment(name) {
			values[name] = value
		}
	}
	if state.term != "" {
		values["TERM"] = state.term
	}
	process.Env = replaceEnvironment(os.Environ(), values)
	if state.term != "" {
		s.runPTY(channel, state, process)
		return
	}
	process.Stdin, process.Stdout, process.Stderr = channel, channel, channel.Stderr()
	err := process.Run()
	s.writeExit(channel, exitStatus(err))
}

func (s *SSHServer) runPTY(channel ssh.Channel, state *sshSessionState, process *exec.Cmd) {
	terminal, err := pty.StartWithSize(process, &pty.Winsize{
		Cols: uint16(state.columns), Rows: uint16(state.rows),
	})
	if err != nil {
		s.writeExit(channel, 1)
		return
	}
	state.process = terminal
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(terminal, channel)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(channel, terminal)
		done <- struct{}{}
	}()
	err = process.Wait()
	_ = terminal.Close()
	<-done
	s.writeExit(channel, exitStatus(err))
}

func (s *SSHServer) writeExit(channel ssh.Channel, status uint32) {
	_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
}

func allowedSSHEnvironment(name string) bool {
	switch name {
	case "LANG", "LC_ALL", "LC_CTYPE", "COLORTERM", "TERM_PROGRAM":
		return true
	default:
		return strings.HasPrefix(name, "LC_")
	}
}

func exitStatus(err error) uint32 {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() >= 0 {
		return uint32(exit.ExitCode())
	}
	return 1
}

func loadOrCreateHostKey(path string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return ssh.ParsePrivateKey(data)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})
	if err := atomicWriteSecret(path, block); err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(privateKey)
}

func atomicWriteSecret(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".host-key-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

type sshChannelConnection struct {
	ssh.Channel
	local  net.Addr
	remote net.Addr
}

func (c *sshChannelConnection) LocalAddr() net.Addr              { return c.local }
func (c *sshChannelConnection) RemoteAddr() net.Addr             { return c.remote }
func (c *sshChannelConnection) SetDeadline(time.Time) error      { return nil }
func (c *sshChannelConnection) SetReadDeadline(time.Time) error  { return nil }
func (c *sshChannelConnection) SetWriteDeadline(time.Time) error { return nil }

type streamAddress string

func (a streamAddress) Network() string { return "ssh" }
func (a streamAddress) String() string  { return string(a) }
