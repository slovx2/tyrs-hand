package hostworker

import "github.com/slovx2/tyrs-hand/internal/runtimeidentity"

type RuntimeInfo struct {
	runtimeidentity.Identity
	ProtocolVersion string   `json:"protocolVersion"`
	Status          string   `json:"status"`
	NodeVersion     string   `json:"nodeVersion,omitempty"`
	SDKVersion      string   `json:"sdkVersion,omitempty"`
	CLIBuild        string   `json:"cliBuild,omitempty"`
	CLISHA256       string   `json:"cliSha256,omitempty"`
	Capabilities    []string `json:"capabilities"`
	ReleaseReady    bool     `json:"releaseReady"`
}
