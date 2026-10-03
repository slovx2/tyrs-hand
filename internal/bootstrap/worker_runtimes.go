package bootstrap

import (
	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
)

// 共用入站授权和出站 SSH Agent；进程、配置、Host Key 与 Controller 分开。
func workerRuntimeEntries(cfg config.Config, codex hostworker.RuntimeOptions,
	ssh hostworker.SSHOptions, claudeController, piController appserverhub.Controller,
) []hostworker.RuntimeEntryOptions {
	entries := []hostworker.RuntimeEntryOptions{{Runtime: codex, SSH: ssh}}
	if cfg.WorkerPiEnabled {
		pi := codex
		pi.Engine = runtimeidentity.Pi
		pi.CodexBin = cfg.WorkerPiBin
		pi.CodexHome = cfg.PiAdapterHome()
		pi.StateDir = cfg.PiStateDir()
		pi.EnvFile = ""
		pi.Controller = piController
		pi.BrowserServiceSocket = ""
		piSSH := ssh
		piSSH.ListenAddr = cfg.WorkerPiSSHListenAddr
		piSSH.HostKeyFile = cfg.PiHostKeyFile()
		piSSH.CodexHome = pi.CodexHome
		piSSH.BrowserProxy = nil
		entries = append(entries, hostworker.RuntimeEntryOptions{Runtime: pi, SSH: piSSH})
	}
	if !cfg.WorkerClaudeEnabled {
		return entries
	}
	claude := codex
	claude.Engine = runtimeidentity.Claude
	claude.CodexBin = cfg.WorkerClaudeBin
	claude.ClaudeCLI = cfg.WorkerClaudeCLI
	claude.CodexHome = cfg.ClaudeAdapterHome()
	// HOME 与 Codex 入口相同，git、gh、ssh 与 Claude 自身配置都读取 Worker 用户的真实 HOME。
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
