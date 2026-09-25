package config

import (
	"errors"
	"net"
	"path/filepath"
)

// Claude 数据路径始终从 Worker 根目录派生，不复用宿主 HOME 或 Codex Home。
func (c Config) ClaudeStateDir() string    { return filepath.Join(c.WorkerDataRoot, "claude-code") }
func (c Config) ClaudeAdapterHome() string { return filepath.Join(c.ClaudeStateDir(), "config") }
func (c Config) ClaudeConfigDir() string   { return filepath.Join(c.ClaudeAdapterHome(), "claude") }
func (c Config) ClaudeHome() string        { return filepath.Join(c.ClaudeStateDir(), "home") }
func (c Config) ClaudeHostKeyFile() string {
	return filepath.Join(c.ClaudeStateDir(), "ssh", "host_key")
}
func (c Config) ClaudeEnvFile() string { return filepath.Join(c.ClaudeStateDir(), "runtime.env") }

func (c Config) validateClaudeRuntime() error {
	if !c.WorkerClaudeEnabled {
		return nil
	}
	if !filepath.IsAbs(c.WorkerClaudeBin) {
		return errors.New("必须为 Claude 适配器配置绝对路径")
	}
	if c.WorkerClaudeSSHListenAddr == "" {
		return errors.New("必须配置 Claude SSH 监听地址")
	}
	claude, err := net.ResolveTCPAddr("tcp", c.WorkerClaudeSSHListenAddr)
	if err != nil {
		return err
	}
	codex, err := net.ResolveTCPAddr("tcp", c.WorkerSSHListenAddr)
	if err != nil {
		return err
	}
	if claude.Port <= 0 || codex.Port <= 0 {
		return errors.New("必须固定 Worker SSH 端口，禁止自动分配")
	}
	if claude.Port == codex.Port && (len(claude.IP) == 0 || len(codex.IP) == 0 || claude.IP.IsUnspecified() || codex.IP.IsUnspecified() || claude.IP.Equal(codex.IP)) {
		return errors.New("两个运行时的 Claude 与 Codex SSH 端口冲突")
	}
	return nil
}
