package hostworker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/protocol"
	"github.com/stretchr/testify/require"
)

func TestClaudeHostCLIEnvironmentUsesWorkerHome(t *testing.T) {
	options := RuntimeOptions{Engine: runtimeidentity.Claude, Home: "/worker", CodexHome: "/worker/config",
		ClaudeCLI: "/opt/host-claude", Environment: []string{"PATH=/bin", "HOME=/personal", "CLAUDE_CONFIG_DIR=/personal/.claude", "ANTHROPIC_API_KEY=personal", "CLAUDE_CODEX_CLI=/wrong"}}
	env := runtimeBaseEnvironment(options)
	for _, want := range []string{"HOME=/worker", "CLAUDE_CODEX_CLI=/opt/host-claude", "DISABLE_AUTOUPDATER=1"} {
		require.Contains(t, env, want)
	}
	require.NotContains(t, strings.Join(env, "\n"), "personal")
	require.NotContains(t, strings.Join(env, "\n"), "CLAUDE_CONFIG_DIR")
	options.ClaudeCLI = ""
	require.Contains(t, runtimeBaseEnvironment(options), "CLAUDE_CODEX_CLI=claude")
}

func TestClaudeBuildRequiresLockedHostCLIVersion(t *testing.T) {
	for _, version := range []string{protocol.AdapterLock.ClaudeCLI + " (Claude Code)", "2.1.283 (Claude Code)", ""} {
		t.Run(version, func(t *testing.T) {
			info := map[string]any{"engine": "claude-code", "protocolVersion": "0.157.1", "nodeVersion": protocol.AdapterLock.Node,
				"sdkVersion": protocol.AdapterLock.ClaudeAgentSDK, "cliBuild": version, "cliSha256": strings.Repeat("a", 64),
				"capabilities": []string{"history.pagination", "submission.idempotency", "dynamicTools", "nativeSession.rollback"}}
			data, err := json.Marshal(info)
			require.NoError(t, err)
			bin := filepath.Join(t.TempDir(), "adapter")
			require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\ncat <<'INFO'\n"+string(data)+"\nINFO\n"), 0o700))
			_, err = validateRuntimeBuild(t.Context(), RuntimeOptions{Engine: runtimeidentity.Claude, CodexBin: bin, Environment: []string{"PATH=/usr/bin:/bin"}})
			if version == protocol.AdapterLock.ClaudeCLI+" (Claude Code)" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "宿主 Claude CLI 版本不符")
			}
		})
	}
}

func TestClaudeBuildReportsMissingHostCLI(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "adapter")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho '宿主 Claude CLI 未找到' >&2\nexit 1\n"), 0o700))
	_, err := validateRuntimeBuild(t.Context(), RuntimeOptions{Engine: runtimeidentity.Claude, CodexBin: bin})
	require.ErrorContains(t, err, "宿主 Claude CLI 未找到")
}
