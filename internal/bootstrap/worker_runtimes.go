package bootstrap

import (
	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
)

// 共用入站授权和出站 SSH Agent；进程、配置、Host Key 与 Controller 分开。
func workerRuntimeEntries(cfg config.Config, codex hostworker.RuntimeOptions,
	ssh hostworker.SSHOptions, claudeController appserverhub.Controller,
) []hostworker.RuntimeEntryOptions {
	entries := []hostworker.RuntimeEntryOptions{{Runtime: codex, SSH: ssh}}
	if !cfg.WorkerClaudeEnabled {
		return entries
	}
	claude := codex
	claude.Engine = runtimeidentity.Claude
	claude.CodexBin = cfg.WorkerClaudeBin
	claude.CodexHome = cfg.ClaudeAdapterHome()
	// HOME 与 Codex 入口相同，git、gh、ssh 等工具读取 Worker 用户的真实配置；
	// Claude 自身配置由 CLAUDE_CONFIG_DIR 隔离，不读取宿主 Claude Code 的 ~/.claude。
	claude.StateDir = cfg.ClaudeStateDir()
	claude.EnvFile = cfg.ClaudeEnvFile()
	claude.Controller = claudeController
	// Browser 服务代理属于整台 Worker，只由 Codex 入口创建一次。
	claude.BrowserServiceSocket = ""
	claudeSSH := ssh
	claudeSSH.ListenAddr = cfg.WorkerClaudeSSHListenAddr
	claudeSSH.HostKeyFile = cfg.ClaudeHostKeyFile()
	claudeSSH.Home = claude.Home
	claudeSSH.CodexHome = claude.CodexHome
	return append(entries, hostworker.RuntimeEntryOptions{Runtime: claude, SSH: claudeSSH})
}
