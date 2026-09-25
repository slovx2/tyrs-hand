package hostworker

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
)

type RuntimeEntryOptions struct {
	Runtime RuntimeOptions
	SSH     SSHOptions
}

type RuntimeEntry struct {
	Runtime *Runtime
	SSH     *SSHServer
}

// RuntimeRegistry 不领取任务；两个引擎共用上层唯一 Runner 及并发配额。
type RuntimeRegistry struct {
	Authorization *ClientAuthorization
	entries       map[runtimeidentity.Engine]*RuntimeEntry
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
}

func StartRuntimeRegistry(ctx context.Context, options []RuntimeEntryOptions) (*RuntimeRegistry, error) {
	if err := validateEntries(options); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	registry := &RuntimeRegistry{entries: make(map[runtimeidentity.Engine]*RuntimeEntry), ctx: ctx, cancel: cancel,
		Authorization: NewClientAuthorization(options[0].SSH.AuthorizedClients)}
	for _, entry := range options {
		runtime, err := startRuntime(ctx, entry.Runtime, true)
		if err != nil {
			_ = registry.Close()
			return nil, err
		}
		entry.SSH.Runtime = runtime
		entry.SSH.Authorization = registry.Authorization
		entry.SSH.RuntimeInfo = runtime.Info
		server, err := StartSSHServer(ctx, entry.SSH)
		if err != nil {
			_ = runtime.Close()
			_ = registry.Close()
			return nil, err
		}
		registry.entries[entry.Runtime.Engine] = &RuntimeEntry{Runtime: runtime, SSH: server}
		registry.wg.Add(1)
		go registry.monitor(ctx, runtime)
	}
	return registry, nil
}

func validateEntries(options []RuntimeEntryOptions) error {
	if len(options) < 1 || len(options) > 2 {
		return errors.New("只能为 Worker 配置一个或两个固定引擎")
	}
	seen := map[runtimeidentity.Engine]bool{}
	workerID := options[0].Runtime.WorkerID
	if workerID == "" {
		return errors.New("必须提供 Worker ID")
	}
	for i, entry := range options {
		if err := entry.Runtime.Engine.Validate(); err != nil {
			return err
		}
		if seen[entry.Runtime.Engine] {
			return errors.New("重复配置引擎")
		}
		seen[entry.Runtime.Engine] = true
		if entry.Runtime.WorkerID != workerID {
			return errors.New("两个引擎必须属于同一个 Worker")
		}
		address, err := net.ResolveTCPAddr("tcp", entry.SSH.ListenAddr)
		if err != nil {
			return fmt.Errorf("SSH 入口地址无效: %w", err)
		}
		for _, previous := range options[:i] {
			if !maps.Equal(authorizedClientKeys(entry.SSH.AuthorizedClients), authorizedClientKeys(previous.SSH.AuthorizedClients)) {
				return errors.New("同一 Worker 的两个 SSH 入口必须共用客户端授权凭证")
			}
			other, err := net.ResolveTCPAddr("tcp", previous.SSH.ListenAddr)
			if err != nil {
				return err
			}
			if address.Port != 0 && address.Port == other.Port &&
				(address.IP.IsUnspecified() || other.IP.IsUnspecified() || len(address.IP) == 0 || len(other.IP) == 0 || address.IP.Equal(other.IP)) {
				return errors.New("两个 SSH 入口地址冲突")
			}
			for _, pair := range [][2]string{{entry.Runtime.StateDir, previous.Runtime.StateDir}, {entry.Runtime.CodexHome, previous.Runtime.CodexHome}, {entry.SSH.HostKeyFile, previous.SSH.HostKeyFile}} {
				left, err := canonicalRuntimePath(pair[0])
				if err != nil {
					return err
				}
				right, err := canonicalRuntimePath(pair[1])
				if err != nil {
					return err
				}
				if left == right {
					return errors.New("两个引擎的状态、配置和 Host Key 必须独立")
				}
			}
			if filepath.Clean(entry.Runtime.WorkspaceRoot) != filepath.Clean(previous.Runtime.WorkspaceRoot) {
				return errors.New("两个引擎必须共享项目目录")
			}
			if entry.Runtime.EnvFile != "" && previous.Runtime.EnvFile != "" {
				left, err := canonicalRuntimePath(entry.Runtime.EnvFile)
				if err != nil {
					return err
				}
				right, err := canonicalRuntimePath(previous.Runtime.EnvFile)
				if err != nil {
					return err
				}
				if left == right {
					return errors.New("模型凭据环境文件必须独立")
				}
			}
		}
	}
	return nil
}

func authorizedClientKeys(clients []AuthorizedClient) map[string]string {
	keys := make(map[string]string, len(clients))
	for _, client := range clients {
		if client.PublicKey != nil {
			keys[string(client.PublicKey.Marshal())] = client.ID
		}
	}
	return keys
}

// 目录可能尚未创建，先解析已存在的父目录，避免符号链接绕过配置隔离。
func canonicalRuntimePath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path, nil
	}
	resolved, err = canonicalRuntimePath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}

func (r *RuntimeRegistry) Entry(engine runtimeidentity.Engine) (*RuntimeEntry, error) {
	if err := engine.Validate(); err != nil {
		return nil, err
	}
	entry := r.entries[engine]
	if entry == nil {
		return nil, fmt.Errorf("引擎 %s 未启用", engine)
	}
	return entry, nil
}

func (r *RuntimeRegistry) Restart(engine runtimeidentity.Engine) error {
	entry, err := r.Entry(engine)
	if err != nil {
		return err
	}
	entry.Runtime.mu.Lock()
	current := entry.Runtime.current
	stopped := generationStopped(current)
	entry.Runtime.mu.Unlock()
	if stopped {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return entry.Runtime.recoverAfterDesktopFailure(ctx, current)
	}
	return entry.Runtime.Restart()
}

func (r *RuntimeRegistry) Close() error {
	r.cancel()
	r.wg.Wait()
	var errs []error
	for _, entry := range r.entries {
		errs = append(errs, entry.SSH.Close(), entry.Runtime.Close())
	}
	return errors.Join(errs...)
}

func (r *RuntimeRegistry) monitor(ctx context.Context, runtime *Runtime) {
	defer r.wg.Done()
	delay := time.Second
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		runtime.mu.Lock()
		failed := runtime.current
		stopped := !runtime.closed && generationStopped(failed)
		runtime.mu.Unlock()
		if !stopped {
			delay = time.Second
			continue
		}
		recovery, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := runtime.recoverAfterDesktopFailure(recovery, failed)
		cancel()
		if err != nil {
			delay = min(delay*2, 30*time.Second)
		} else {
			delay = time.Second
		}
	}
}
