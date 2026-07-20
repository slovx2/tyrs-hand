package hostdocker

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type fakeDockerRunner struct {
	mu      sync.Mutex
	calls   [][]string
	respond func([]string) (string, error)
}

func (r *fakeDockerRunner) Output(_ context.Context, arguments ...string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{}, arguments...))
	return r.respond(arguments)
}

func testManager(t *testing.T, runner commandRunner) *Manager {
	t.Helper()
	return &Manager{
		enabled: true, network: "runtime", root: t.TempDir(), leaseDuration: time.Minute,
		stopTimeout: time.Second, cleanupTimeout: time.Second, sweepInterval: time.Hour,
		runner: runner, logger: zap.NewNop(), expectedVersion: "28.3.3",
	}
}

func testScope() Scope {
	return Scope{WorkspaceID: uuid.NewString(), IntentID: uuid.NewString(), RunID: uuid.NewString()}
}

func TestDisabledManagerAndSessionEnvironment(t *testing.T) {
	disabled, err := NewManager(config.Config{
		WorkerDataRoot: t.TempDir(), LeaseDuration: time.Minute, DockerNetwork: "runtime",
		DockerStopTimeout: time.Second, DockerCleanupTimeout: time.Second, DockerSweepInterval: time.Second,
	}, zap.NewNop())
	require.NoError(t, err)
	require.False(t, disabled.Enabled())
	session, err := disabled.Begin(testScope())
	require.NoError(t, err)
	require.Nil(t, session)

	manager := testManager(t, &fakeDockerRunner{respond: func([]string) (string, error) { return "", nil }})
	scope := testScope()
	session, err = manager.Begin(scope)
	require.NoError(t, err)
	environment := strings.Join(session.Environment(), "\n")
	require.Contains(t, environment, "DOCKER_HOST=unix:///var/run/docker.sock")
	require.Contains(t, environment, "TYRS_HAND_DOCKER_WORKSPACE_ID="+scope.WorkspaceID)
	require.Contains(t, environment, "TYRS_HAND_DOCKER_NAME_PREFIX=th-")
}

func TestTouchAndSweeperRecoverExpiredLease(t *testing.T) {
	runner := &fakeDockerRunner{respond: func(arguments []string) (string, error) {
		joined := strings.Join(arguments, " ")
		switch {
		case strings.HasPrefix(joined, "container ls"):
			return "expired", nil
		case strings.HasPrefix(joined, "container inspect"):
			return "true|true", nil
		case strings.HasPrefix(joined, "container stop"):
			return "expired", nil
		default:
			return "", nil
		}
	}}
	manager := testManager(t, runner)
	scope := testScope()
	session, err := manager.Begin(scope)
	require.NoError(t, err)
	before, err := readLease(manager.root, scope.RunID)
	require.NoError(t, err)
	time.Sleep(time.Millisecond)
	require.NoError(t, manager.Touch(scope.RunID))
	after, err := readLease(manager.root, scope.RunID)
	require.NoError(t, err)
	require.True(t, after.ExpiresAt.After(before.ExpiresAt))
	require.NoError(t, updateLease(manager.root, scope.RunID, func(lease *runLease) error {
		lease.ExpiresAt = time.Now().Add(-time.Second)
		return nil
	}))
	manager.sweep(context.Background())
	_, err = os.Stat(leasePath(manager.root, scope.RunID))
	require.ErrorIs(t, err, os.ErrNotExist)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manager.RunSweeper(ctx)
	_ = session
}

func TestSessionStopsManagedContainerWithoutDeletingResources(t *testing.T) {
	runner := &fakeDockerRunner{respond: func(arguments []string) (string, error) {
		joined := strings.Join(arguments, " ")
		switch {
		case strings.HasPrefix(joined, "container ls"):
			return "created-container", nil
		case strings.HasPrefix(joined, "container inspect"):
			return "true|true", nil
		case strings.HasPrefix(joined, "container stop"):
			return "created-container", nil
		default:
			return "", errors.New("unexpected command: " + joined)
		}
	}}
	manager := testManager(t, runner)
	session, err := manager.Begin(testScope())
	require.NoError(t, err)
	require.NoError(t, session.Close(context.Background()))
	_, err = os.Stat(leasePath(manager.root, session.scope.RunID))
	require.ErrorIs(t, err, os.ErrNotExist)
	for _, call := range runner.calls {
		joined := strings.Join(call, " ")
		require.NotContains(t, joined, " rm ")
		require.NotContains(t, joined, " prune")
	}
}

func TestCleanupIsIdempotentWhenContainerWasAlreadyRemoved(t *testing.T) {
	runner := &fakeDockerRunner{respond: func(arguments []string) (string, error) {
		if len(arguments) > 1 && arguments[1] == "ls" {
			return "", nil
		}
		return "Error: No such container: gone", errors.New("exit status 1")
	}}
	manager := testManager(t, runner)
	scope := testScope()
	session, err := manager.Begin(scope)
	require.NoError(t, err)
	require.NoError(t, recordContainers(manager.root, scope.RunID, []string{"gone"}))
	require.NoError(t, session.Close(context.Background()))
	require.NoError(t, session.Close(context.Background()))
}

func TestCleanupFailureWarnsAndLeavesLeaseForRetry(t *testing.T) {
	runner := &fakeDockerRunner{respond: func(arguments []string) (string, error) {
		joined := strings.Join(arguments, " ")
		switch {
		case strings.HasPrefix(joined, "container ls"):
			return "busy", nil
		case strings.HasPrefix(joined, "container inspect"):
			return "true|true", nil
		case strings.HasPrefix(joined, "container stop"):
			return "conflict: container is in use", errors.New("exit status 1")
		default:
			return "", nil
		}
	}}
	manager := testManager(t, runner)
	session, err := manager.Begin(testScope())
	require.NoError(t, err)
	err = session.Close(context.Background())
	require.ErrorContains(t, err, "in use")
	_, statErr := os.Stat(leasePath(manager.root, session.scope.RunID))
	require.NoError(t, statErr)
}

func TestConcurrentRunLeasePreventsEarlyStop(t *testing.T) {
	runner := &fakeDockerRunner{respond: func(arguments []string) (string, error) {
		if len(arguments) > 1 && arguments[1] == "ls" {
			return "shared", nil
		}
		return "true|true", nil
	}}
	manager := testManager(t, runner)
	first, err := manager.Begin(testScope())
	require.NoError(t, err)
	second, err := manager.Begin(testScope())
	require.NoError(t, err)
	require.NoError(t, recordContainers(manager.root, first.scope.RunID, []string{"shared"}))
	require.NoError(t, recordContainers(manager.root, second.scope.RunID, []string{"shared"}))
	require.NoError(t, first.Close(context.Background()))
	for _, call := range runner.calls {
		require.False(t, len(call) > 1 && call[1] == "stop")
	}
	require.NoError(t, second.Close(context.Background()))
	stopped := false
	for _, call := range runner.calls {
		stopped = stopped || (len(call) > 1 && call[1] == "stop")
	}
	require.True(t, stopped)
}

func TestCleanupNeverStopsUnmanagedOrAlreadyStoppedContainer(t *testing.T) {
	responses := []string{"false|true", "true|false"}
	runner := &fakeDockerRunner{respond: func(arguments []string) (string, error) {
		if len(arguments) > 1 && arguments[1] == "ls" {
			return "", nil
		}
		if len(arguments) > 1 && arguments[1] == "inspect" {
			result := responses[0]
			responses = responses[1:]
			return result, nil
		}
		return "", errors.New("不应停止容器")
	}}
	manager := testManager(t, runner)
	scope := testScope()
	session, err := manager.Begin(scope)
	require.NoError(t, err)
	require.NoError(t, recordContainers(manager.root, scope.RunID, []string{"external", "stopped"}))
	require.NoError(t, session.Close(context.Background()))
}
