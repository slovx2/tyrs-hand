package hostworker

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/protocol"
)

func loadClaudeEnvironment(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("Claude 环境文件格式无效")
		}
		switch name {
		case "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "DISABLE_AUTOUPDATER", "DISABLE_TELEMETRY", "DISABLE_ERROR_REPORTING":
		default:
			return nil, fmt.Errorf("Claude 环境文件不允许变量 %s", name)
		}
		if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
			value = value[1 : len(value)-1]
		}
		result[name] = value
	}
	return result, nil
}

func validateRuntimeBuild(ctx context.Context, options RuntimeOptions) (RuntimeInfo, error) {
	info := RuntimeInfo{Identity: runtimeidentity.Identity{WorkerID: options.WorkerID, Engine: options.Engine}, ProtocolVersion: codex.RequiredVersion}
	if err := options.Engine.Validate(); err != nil {
		return info, err
	}
	if options.Engine == runtimeidentity.Codex {
		info.ReleaseReady = true
		info.Capabilities = []string{"nativeSession.revert.paginated", "nativeSession.rollback.paginated"}
		var err error
		info.CLIBuild, err = codex.ValidatedVersion(ctx, options.CodexBin)
		return info, err
	}
	command := exec.CommandContext(ctx, options.CodexBin, "--runtime-info")
	command.Env = runtimeBaseEnvironment(options)
	data, err := command.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && len(exit.Stderr) > 0 {
			return info, fmt.Errorf("读取 %s 构建身份: %w: %s", options.Engine, err, strings.TrimSpace(string(exit.Stderr)))
		}
		return info, fmt.Errorf("读取 %s 构建身份: %w", options.Engine, err)
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return info, err
	}
	if options.Engine == runtimeidentity.Pi {
		if info.Engine != runtimeidentity.Pi || info.ProtocolVersion != codex.RequiredVersion ||
			info.NodeVersion != protocol.AdapterLock.Node || info.SDKVersion != protocol.AdapterLock.PiCodingAgent || info.CLIBuild != protocol.AdapterLock.PiCLI ||
			info.PluginVersions["@narumitw/pi-plan-mode"] != protocol.AdapterLock.PiPlanMode ||
			info.PluginVersions["@narumitw/pi-tui-kit"] != protocol.AdapterLock.PiTuiKit ||
			info.PluginVersions["@gotgenes/pi-subagents"] != protocol.AdapterLock.PiSubagents {
			return info, fmt.Errorf("Pi 构建不符合固定版本组合")
		}
	} else if info.Engine != runtimeidentity.Claude || info.ProtocolVersion != codex.RequiredVersion ||
		info.NodeVersion != protocol.AdapterLock.Node || info.SDKVersion != protocol.AdapterLock.ClaudeAgentSDK ||
		len(info.CLISHA256) != 64 {
		return info, fmt.Errorf("Claude 构建不符合固定版本组合")
	}
	if options.Engine == runtimeidentity.Claude && info.CLIBuild != protocol.AdapterLock.ClaudeCLI+" (Claude Code)" {
		return info, fmt.Errorf("宿主 Claude CLI 版本不符: 需要 %s (Claude Code)，实际 %s", protocol.AdapterLock.ClaudeCLI, info.CLIBuild)
	}
	for _, capability := range []string{"history.pagination", "submission.idempotency", "dynamicTools", "nativeSession.rollback"} {
		if !slices.Contains(info.Capabilities, capability) {
			return info, fmt.Errorf("%s 缺少必需能力 %s", options.Engine, capability)
		}
	}
	info.WorkerID = options.WorkerID
	return info, nil
}

// Claude 不继承宿主模型凭据；provider 由独立目录的原生 settings.json 载入。
func runtimeBaseEnvironment(options RuntimeOptions) []string {
	if options.Engine == runtimeidentity.Pi {
		return piEnvironment(options.Environment)
	}
	if options.Engine != runtimeidentity.Claude {
		return appServerEnvironment(options.Environment)
	}
	result := make([]string, 0)
	for _, entry := range options.Environment {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "PATH", "LANG", "LC_ALL", "LC_CTYPE", "TERM", "TMPDIR", "TEMP", "TMP", "SystemRoot":
			result = append(result, entry)
		}
	}
	cli := options.ClaudeCLI
	if cli == "" {
		cli = "claude"
	}
	return replaceEnvironment(result, map[string]string{
		"HOME":                options.Home,
		"CLAUDE_CODEX_CLI":    cli,
		"DISABLE_AUTOUPDATER": "1",
	})
}

// 保留 Pi 原生 provider 和代理配置，只移除 Worker 内部身份与服务凭据。
func piEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "TYRS_HAND_") || name == codex.BrowserMCPWorkerTokenEnvironment ||
			name == codex.BrowserMCPDesktopTokenEnvironment {
			continue
		}
		result = append(result, entry)
	}
	return result
}

func (r *Runtime) Info() RuntimeInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	info := r.info
	info.Capabilities = slices.Clone(info.Capabilities)
	info.PluginVersions = maps.Clone(info.PluginVersions)
	info.Status = "running"
	if r.closed {
		info.Status = "stopped"
	} else if generationStopped(r.current) {
		info.Status = "unavailable"
	}
	return info
}
