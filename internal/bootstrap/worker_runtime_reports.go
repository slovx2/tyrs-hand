package bootstrap

import (
	"encoding/json"

	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/worker"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func runtimeReports(registry *hostworker.RuntimeRegistry, processors map[runtimeidentity.Engine]*worker.Processor) []workerprotocol.RuntimeReport {
	reports := make([]workerprotocol.RuntimeReport, 0, 2)
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		entry, err := registry.Entry(engine)
		if err != nil {
			continue
		}
		info := entry.Runtime.Info()
		capabilities := append([]string{}, info.Capabilities...)
		var catalog json.RawMessage
		if processor := processors[engine]; processor != nil {
			catalog, _ = processor.HeartbeatMetadata()["modelCatalog"].(json.RawMessage)
		}
		reports = append(reports, workerprotocol.RuntimeReport{
			Engine: engine, Status: info.Status, SSHListenAddress: entry.SSH.Addr().String(),
			SSHHostKeyFingerprint: entry.SSH.HostKeyFingerprint(), ProtocolVersion: info.ProtocolVersion,
			Build: workerprotocol.RuntimeBuild{NodeVersion: info.NodeVersion, SDKVersion: info.SDKVersion,
				CLIBuild: info.CLIBuild, CLISHA256: info.CLISHA256},
			Capabilities: capabilities, ModelCatalog: catalog, ReleaseReady: info.ReleaseReady,
		})
	}
	return reports
}
