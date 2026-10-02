package config

import (
	"errors"
	"net"
	"path/filepath"
)

// Claude 适配器状态从 Worker 根目录派生；Claude 自身配置使用宿主用户的 ~/.claude。
func (c Config) ClaudeStateDir() string {
	return filepath.Join(c.WorkerDataRoot, "codex-harness-adapter", "claude-code")
}
func (c Config) ClaudeAdapterHome() string { return filepath.Join(c.ClaudeStateDir(), "config") }
func (c Config) ClaudeConfigDir() string   { return filepath.Join(c.WorkerHome, ".claude") }
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
