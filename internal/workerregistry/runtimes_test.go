package workerregistry

import (
	"testing"

	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestRuntimeReportsRejectAmbiguousIdentity(t *testing.T) {
	const key = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	valid := workerprotocol.RuntimeReport{Engine: runtimeidentity.Codex,
		SSHListenAddress: ":2222", SSHHostKeyFingerprint: key, Status: "running",
		ProtocolVersion: "0.147.0", Capabilities: []string{},
		Build: workerprotocol.RuntimeBuild{CLIBuild: "0.147.0"}}
	require.NoError(t, validateRuntimeReports([]workerprotocol.RuntimeReport{valid}, key))
	for _, test := range []struct {
		name   string
		change func(*workerprotocol.RuntimeReport)
	}{
		{"unknown engine", func(r *workerprotocol.RuntimeReport) { r.Engine = "unknown" }},
		{"missing engine", func(r *workerprotocol.RuntimeReport) { r.Engine = "" }},
		{"unknown status", func(r *workerprotocol.RuntimeReport) { r.Status = "online" }},
		{"automatic port", func(r *workerprotocol.RuntimeReport) { r.SSHListenAddress = ":0" }},
		{"invalid port", func(r *workerprotocol.RuntimeReport) { r.SSHListenAddress = ":70000" }},
		{"missing build", func(r *workerprotocol.RuntimeReport) { r.Build.CLIBuild = "" }},
		{"missing protocol", func(r *workerprotocol.RuntimeReport) { r.ProtocolVersion = "" }},
		{"missing capabilities", func(r *workerprotocol.RuntimeReport) { r.Capabilities = nil }},
		{"scalar catalog", func(r *workerprotocol.RuntimeReport) { r.ModelCatalog = []byte(`"wrong"`) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := valid
			test.change(&report)
			require.ErrorIs(t, validateRuntimeReports([]workerprotocol.RuntimeReport{report}, key), ErrInvalidRuntimeReport)
		})
	}
	require.ErrorIs(t, validateRuntimeReports([]workerprotocol.RuntimeReport{valid, valid}, key), ErrInvalidRuntimeReport)
	claude := valid
	claude.Engine = runtimeidentity.Claude
	claude.SSHListenAddress = ":3333"
	require.ErrorIs(t, validateRuntimeReports([]workerprotocol.RuntimeReport{valid, claude}, key), ErrInvalidRuntimeReport)
	claude.SSHHostKeyFingerprint = "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	claude.Status = "unavailable"
	claude.Build = workerprotocol.RuntimeBuild{}
	require.NoError(t, validateRuntimeReports([]workerprotocol.RuntimeReport{valid, claude}, key), "制品缺失不能阻止健康引擎心跳")
	claude.SSHListenAddress = ":2222"
	require.ErrorIs(t, validateRuntimeReports([]workerprotocol.RuntimeReport{valid, claude}, key), ErrInvalidRuntimeReport)
}
