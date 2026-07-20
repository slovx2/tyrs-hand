//go:build manual_host_docker

package hostdocker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const manualRedisImage = "redis:8.4.0-bookworm@sha256:c22af04bb576503bf16b3e34a1fd2fd82de0f765afd866d2e380145e0af30d78"

type systemDockerRunner struct{ binary string }

func (r systemDockerRunner) Output(ctx context.Context, arguments ...string) (string, error) {
	output, err := exec.CommandContext(ctx, r.binary, arguments...).CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func TestManualHostDockerStopsAndRestartsPersistentContainer(t *testing.T) {
	if os.Getenv("TYRS_HAND_TEST_HOST_DOCKER") != "1" {
		t.Skip("手动 Host Docker E2E；设置 TYRS_HAND_TEST_HOST_DOCKER=1 后运行")
	}
	binary, err := exec.LookPath("docker")
	require.NoError(t, err)
	suffix := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	network, volume, name := "th-manual-net-"+suffix, "th-manual-data-"+suffix, "th-manual-redis-"+suffix
	runner := systemDockerRunner{binary: binary}
	t.Cleanup(func() {
		_, _ = runner.Output(context.Background(), "container", "rm", "--force", name)
		_, _ = runner.Output(context.Background(), "volume", "rm", volume)
		_, _ = runner.Output(context.Background(), "network", "rm", network)
	})

	manager := &Manager{
		enabled: true, network: network, root: t.TempDir(), leaseDuration: time.Minute,
		stopTimeout: 3 * time.Second, cleanupTimeout: 15 * time.Second,
		sweepInterval: time.Hour, runner: runner, logger: zap.NewNop(),
	}
	workspaceID := uuid.NewString()
	first := Scope{WorkspaceID: workspaceID, IntentID: uuid.NewString(), RunID: uuid.NewString()}
	session, err := manager.Begin(first)
	require.NoError(t, err)
	configureManualWrapper(t, binary, manager.root, network, first)
	require.Equal(t, 0, runManualWrapper(t, "network", "create", network))
	require.Equal(t, 0, runManualWrapper(t, "volume", "create", volume))
	require.Equal(t, 0, runManualWrapper(t, "run", "--detach", "--name", name,
		"--mount", "type=volume,src="+volume+",dst=/data", manualRedisImage,
		"redis-server", "--appendonly", "yes"))
	require.Equal(t, 0, runManualWrapper(t, "exec", name, "redis-cli", "set", "tyrs-hand", "persistent"))
	require.NoError(t, session.Close(context.Background()))
	status, err := runner.Output(context.Background(), "inspect", "--format", "{{.State.Status}}", name)
	require.NoError(t, err)
	require.Equal(t, "exited", status)

	second := Scope{WorkspaceID: workspaceID, IntentID: uuid.NewString(), RunID: uuid.NewString()}
	session, err = manager.Begin(second)
	require.NoError(t, err)
	configureManualWrapper(t, binary, manager.root, network, second)
	require.Equal(t, 0, runManualWrapper(t, "start", name))
	var output bytes.Buffer
	require.Equal(t, 0, RunWrapper(context.Background(), []string{"exec", name, "redis-cli", "get", "tyrs-hand"}, nil, &output, &bytes.Buffer{}))
	require.Contains(t, output.String(), "persistent")
	require.NoError(t, session.Close(context.Background()))
	_, err = runner.Output(context.Background(), "volume", "inspect", volume)
	require.NoError(t, err)
	_, err = runner.Output(context.Background(), "network", "inspect", network)
	require.NoError(t, err)
}

func configureManualWrapper(t *testing.T, binary, root, network string, scope Scope) {
	t.Helper()
	t.Setenv("TYRS_HAND_DOCKER_REAL_BIN", binary)
	t.Setenv(envLeaseRoot, root)
	t.Setenv(envWorkspaceID, scope.WorkspaceID)
	t.Setenv(envIntentID, scope.IntentID)
	t.Setenv(envRunID, scope.RunID)
	t.Setenv("TYRS_HAND_DOCKER_NETWORK", network)
}

func runManualWrapper(t *testing.T, arguments ...string) int {
	t.Helper()
	var output bytes.Buffer
	code := RunWrapper(context.Background(), arguments, nil, &output, &output)
	if code != 0 {
		t.Log(fmt.Sprintf("docker %v: %s", arguments, output.String()))
	}
	return code
}
