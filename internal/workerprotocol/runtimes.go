package workerprotocol

import (
	"encoding/json"

	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
)

// RuntimeReport 是一次完整快照；未列出的引擎被停用，但保留历史和指纹。
type RuntimeReport struct {
	Engine                runtimeidentity.Engine `json:"engine"`
	Status                string                 `json:"status"`
	SSHListenAddress      string                 `json:"sshListenAddress"`
	SSHHostKeyFingerprint string                 `json:"sshHostKeyFingerprint"`
	ProtocolVersion       string                 `json:"protocolVersion"`
	Build                 RuntimeBuild           `json:"build"`
	Capabilities          []string               `json:"capabilities"`
	ModelCatalog          json.RawMessage        `json:"modelCatalog"`
	ReleaseReady          bool                   `json:"releaseReady"`
}

type RuntimeBuild struct {
	NodeVersion string `json:"nodeVersion,omitempty"`
	SDKVersion  string `json:"sdkVersion,omitempty"`
	CLIBuild    string `json:"cliBuild"`
	CLISHA256   string `json:"cliSha256,omitempty"`
}
