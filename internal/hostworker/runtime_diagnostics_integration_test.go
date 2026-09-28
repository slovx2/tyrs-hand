//go:build integration

package hostworker

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeDiagnosticsRealSSHBothEngines(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "diagnostics")
}

// DIAGNOSTICS-002：通过真实 SSH 查询所属进程，完整重启一端不会替换另一端。
func verifyRuntimeDiagnostics(t *testing.T, ctx context.Context, registry *RuntimeRegistry, connections map[runtimeidentity.Engine]*ssh.Client, clients map[runtimeidentity.Engine]*codex.SocketClient) {
	t.Helper()
	read := func(engine runtimeidentity.Engine) int {
		var result struct {
			Process struct {
				ID                     int
				ResidentMemoryBytes    *uint64
				PhysicalFootprintBytes *uint64
			}
			Gauges []struct {
				Name  string
				Value float64
			}
		}
		require.NoError(t, clients[engine].Call(ctx, "server/diagnostics", map[string]any{}, &result))
		require.Positive(t, result.Process.ID)
		process, err := os.FindProcess(result.Process.ID)
		require.NoError(t, err)
		require.NoError(t, process.Signal(syscall.Signal(0)), "返回的进程必须实际存活")
		require.NoError(t, process.Release())
		if runtime.GOOS == "linux" {
			command, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", result.Process.ID))
			require.NoError(t, err)
			if engine == runtimeidentity.Claude {
				require.Contains(t, string(command), "adapter.mjs")
			} else {
				require.Contains(t, string(command), "codex")
				require.Contains(t, string(command), "app-server")
				require.NotContains(t, string(command), "adapter.mjs")
			}
			require.NotNil(t, result.Process.ResidentMemoryBytes)
			require.Positive(t, *result.Process.ResidentMemoryBytes)
		}
		seen := map[string]bool{}
		for _, gauge := range result.Gauges {
			require.NotEmpty(t, strings.TrimSpace(gauge.Name))
			require.False(t, seen[gauge.Name], "诊断计数名称不能重复")
			seen[gauge.Name] = true
		}
		return result.Process.ID
	}
	engines := []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude}
	pids := map[runtimeidentity.Engine]int{}
	for _, engine := range engines {
		pids[engine] = read(engine)
		require.Equal(t, pids[engine], read(engine), "只读查询不能替换进程")
	}
	require.NotEqual(t, pids[runtimeidentity.Codex], pids[runtimeidentity.Claude])
	for _, engine := range engines {
		other := runtimeidentity.Codex
		if engine == other {
			other = runtimeidentity.Claude
		}
		generation := registry.entries[other].Runtime.Generation()
		require.NoError(t, registry.Restart(engine))
		clients[engine] = connectRuntimeSSH(t, ctx, connections[engine], engine)
		pid := read(engine)
		require.NotEqual(t, pids[engine], pid, "重启后必须反映新进程而非缓存")
		pids[engine] = pid
		require.Equal(t, pids[other], read(other))
		require.Equal(t, generation, registry.entries[other].Runtime.Generation())
	}
}
