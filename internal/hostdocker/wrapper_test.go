package hostdocker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func testWrapperScope(t *testing.T) wrapperScope {
	t.Helper()
	return wrapperScope{
		Scope:     Scope{WorkspaceID: uuid.NewString(), IntentID: uuid.NewString(), RunID: uuid.NewString()},
		LeaseRoot: t.TempDir(), Network: "tyrs-hand-agent-runtime",
	}
}

func TestPrepareArgumentsInjectsLabelsAndNetwork(t *testing.T) {
	scope := testWrapperScope(t)
	for _, input := range [][]string{
		{"run", "--name", "db", "postgres:18.3"},
		{"container", "create", "redis:8.4.0"},
		{"--context", "default", "run", "alpine:3.22"},
	} {
		prepared := prepareArguments(input, scope)
		require.Contains(t, prepared.Arguments, managedLabel+"=true")
		require.Contains(t, prepared.Arguments, workspaceLabel+"="+scope.WorkspaceID)
		require.Contains(t, prepared.Arguments, "--network")
		require.Contains(t, prepared.Arguments, scope.Network)
	}

	prepared := prepareArguments([]string{"run", "--network", "custom", "redis:8.4.0"}, scope)
	require.Equal(t, 1, countArgument(prepared.Arguments, "--network"))
	require.Contains(t, prepared.Arguments, "custom")
}

func TestPrepareArgumentsHandlesResourcesAndPassthrough(t *testing.T) {
	scope := testWrapperScope(t)
	for _, input := range [][]string{{"volume", "create", "data"}, {"network", "create", "backend"}} {
		prepared := prepareArguments(input, scope)
		require.Contains(t, prepared.Arguments, managedLabel+"=true")
		require.NotContains(t, prepared.Arguments, "--network")
	}
	input := []string{"ps", "--format", "{{.ID}}"}
	require.Equal(t, input, prepareArguments(input, scope).Arguments)
}

func TestPrepareArgumentsTracksContainerUse(t *testing.T) {
	scope := testWrapperScope(t)
	require.Equal(t, []string{"one", "two"}, prepareArguments(
		[]string{"container", "start", "--attach", "one", "two"}, scope).TouchTargets)
	require.Equal(t, []string{"one"}, prepareArguments(
		[]string{"exec", "--env", "A=B", "one", "sh", "-lc", "true"}, scope).TouchTargets)
	require.Equal(t, []string{"one"}, prepareArguments(
		[]string{"restart", "--time", "5", "one"}, scope).TouchTargets)
}

func TestWrapperRecordsOnlySuccessfulUse(t *testing.T) {
	scope := testWrapperScope(t)
	lease := runLease{Scope: scope.Scope, ExpiresAt: time.Now().Add(time.Minute)}
	require.NoError(t, os.MkdirAll(scope.LeaseRoot, 0o750))
	require.NoError(t, writeLease(scope.LeaseRoot, lease))
	configureWrapperEnvironment(t, scope)

	failure := writeFakeDocker(t, "exit 7\n")
	t.Setenv("TYRS_HAND_DOCKER_REAL_BIN", failure)
	require.Equal(t, 7, RunWrapper(context.Background(), []string{"start", "db"}, nil, &bytes.Buffer{}, &bytes.Buffer{}))
	actual, err := readLease(scope.LeaseRoot, scope.RunID)
	require.NoError(t, err)
	require.Empty(t, actual.Containers)

	success := writeFakeDocker(t, "if [ \"$1\" = container ] && [ \"$2\" = inspect ]; then echo container-id; fi\nexit 0\n")
	t.Setenv("TYRS_HAND_DOCKER_REAL_BIN", success)
	require.Equal(t, 0, RunWrapper(context.Background(), []string{"start", "db"}, nil, &bytes.Buffer{}, &bytes.Buffer{}))
	actual, err = readLease(scope.LeaseRoot, scope.RunID)
	require.NoError(t, err)
	require.Equal(t, []string{"container-id"}, actual.Containers)
}

func configureWrapperEnvironment(t *testing.T, scope wrapperScope) {
	t.Helper()
	t.Setenv(envLeaseRoot, scope.LeaseRoot)
	t.Setenv(envWorkspaceID, scope.WorkspaceID)
	t.Setenv(envIntentID, scope.IntentID)
	t.Setenv(envRunID, scope.RunID)
	t.Setenv("TYRS_HAND_DOCKER_NETWORK", scope.Network)
}

func writeFakeDocker(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700))
	return path
}

func countArgument(arguments []string, target string) int {
	count := 0
	for _, argument := range arguments {
		if argument == target {
			count++
		}
	}
	return count
}
