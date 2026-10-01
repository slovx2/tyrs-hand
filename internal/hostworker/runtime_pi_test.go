package hostworker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

func TestPiEnvironmentKeepsNativeProvidersAndRemovesWorkerCredentials(t *testing.T) {
	input := []string{"HOME=/user", "PI_CODING_AGENT_DIR=/native/pi", "OPENAI_API_KEY=test-only",
		"ANTHROPIC_API_KEY=test-only", "HTTPS_PROXY=http://proxy.invalid", "TYRS_HAND_WORKER_CREDENTIAL=internal"}
	result := runtimeBaseEnvironment(RuntimeOptions{Engine: runtimeidentity.Pi, Environment: input})
	require.Equal(t, input[:5], result)
}

func TestValidatePiRuntimeBuildRejectsVersionDrift(t *testing.T) {
	for _, field := range []string{"valid", "engine", "protocolVersion", "nodeVersion", "sdkVersion", "cliBuild",
		"@narumitw/pi-plan-mode", "@narumitw/pi-tui-kit", "@gotgenes/pi-subagents"} {
		t.Run(field, func(t *testing.T) {
			plugins := map[string]string{"@narumitw/pi-plan-mode": "0.58.3", "@narumitw/pi-tui-kit": "0.59.0", "@gotgenes/pi-subagents": "21.8.1"}
			info := map[string]any{"engine": "pi", "protocolVersion": "0.157.1", "nodeVersion": "24.14.0", "sdkVersion": "0.99.1", "cliBuild": "0.99.1",
				"pluginVersions": plugins, "capabilities": []string{"history.pagination", "submission.idempotency", "dynamicTools", "nativeSession.rollback"}}
			if _, ok := plugins[field]; ok {
				plugins[field] = "0.0.0"
			} else if field != "valid" {
				info[field] = "wrong"
			}
			data, err := json.Marshal(info)
			require.NoError(t, err)
			bin := filepath.Join(t.TempDir(), "pi-fixture")
			require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\ncat <<'INFO'\n"+string(data)+"\nINFO\n"), 0o700))
			_, err = validateRuntimeBuild(t.Context(), RuntimeOptions{Engine: runtimeidentity.Pi, CodexBin: bin, Environment: []string{"PATH=/usr/bin:/bin"}})
			if field == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestRuntimeRegistryAcceptsThreeIsolatedEngines(t *testing.T) {
	root := t.TempDir()
	entries := registryFixture(root)
	pi := entries[1]
	pi.Runtime.Engine = runtimeidentity.Pi
	pi.Runtime.StateDir = filepath.Join(root, "pi", "state")
	pi.Runtime.CodexHome = filepath.Join(root, "pi", "metadata")
	pi.Runtime.EnvFile = filepath.Join(root, "pi", "runtime.env")
	pi.SSH.ListenAddr = "127.0.0.1:3334"
	pi.SSH.HostKeyFile = filepath.Join(root, "pi", "host_key")
	entries = append(entries, pi)
	require.NoError(t, validateEntries(entries))
	entries[2].SSH.ListenAddr = ":3333"
	require.Error(t, validateEntries(entries))
}
