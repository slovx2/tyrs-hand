package hostworker

import "github.com/slovx2/tyrs-hand/internal/runtimeidentity"

type RuntimeInfo struct {
	runtimeidentity.Identity
	ProtocolVersion string            `json:"protocolVersion"`
	Status          string            `json:"status"`
	NodeVersion     string            `json:"nodeVersion,omitempty"`
	SDKVersion      string            `json:"sdkVersion,omitempty"`
	PluginVersions  map[string]string `json:"pluginVersions,omitempty"`
	CLIBuild        string            `json:"cliBuild,omitempty"`
	CLISHA256       string            `json:"cliSha256,omitempty"`
	Capabilities    []string          `json:"capabilities"`
	ReleaseReady    bool              `json:"releaseReady"`
}

// CodexVersion 是 SSH 入口对 codex --version 的回报，须与 App Server initialize
// 的 userAgent 版本一致，否则 Desktop 判定版本不符并提示重启。原生 Codex 回报
// 真实 CLI 版本，适配器运行时回报其实现的协议版本。
func (i RuntimeInfo) CodexVersion() string {
	if i.Engine == runtimeidentity.Codex && i.CLIBuild != "" {
		return i.CLIBuild
	}
	return i.ProtocolVersion
}
