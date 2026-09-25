package hostworker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
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
		var err error
		info.CLIBuild, err = codex.ValidatedVersion(ctx, options.CodexBin)
		return info, err
	}
	command := exec.CommandContext(ctx, options.CodexBin, "--runtime-info")
	command.Env = runtimeBaseEnvironment(options)
	data, err := command.Output()
	if err != nil {
		return info, fmt.Errorf("读取 Claude 构建身份: %w", err)
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return info, err
	}
	if info.Engine != runtimeidentity.Claude || info.ProtocolVersion != codex.RequiredVersion ||
		info.NodeVersion != "24.14.0" || info.SDKVersion != "0.3.282" ||
		info.CLIBuild == "" || len(info.CLISHA256) != 64 {
		return info, fmt.Errorf("Claude 构建不符合固定版本组合")
	}
	for _, capability := range []string{"history.pagination", "submission.idempotency", "dynamicTools", "nativeSession.rollback"} {
		if !slices.Contains(info.Capabilities, capability) {
			return info, fmt.Errorf("Claude 缺少必需能力 %s", capability)
		}
	}
	info.WorkerID = options.WorkerID
	return info, nil
}

// Claude 不继承宿主模型凭据；provider 由独立目录的原生 settings.json 载入。
func runtimeBaseEnvironment(options RuntimeOptions) []string {
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
	return result
}

func (r *Runtime) Info() RuntimeInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	info := r.info
	info.Capabilities = slices.Clone(info.Capabilities)
	info.Status = "running"
	if r.closed {
		info.Status = "stopped"
	} else if generationStopped(r.current) {
		info.Status = "unavailable"
	}
	return info
}
