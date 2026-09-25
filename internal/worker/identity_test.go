package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestRegisteredWorkerIdentitySurvivesOfflineRestart(t *testing.T) {
	id := uuid.New()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "/worker/v1/identity", r.URL.Path)
		require.Equal(t, "Bearer virtual-worker-credential", r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(workerprotocol.WorkerIdentityResponse{WorkerID: id, ProtocolVersion: workerprotocol.Version})
	}))
	t.Cleanup(server.Close)
	cfg := config.Config{WorkerID: "可修改的机器名", WorkerDataRoot: t.TempDir(), WorkerControlURL: server.URL}
	cfg.WorkerCredentialFile = filepath.Join(cfg.WorkerDataRoot, "credential")
	require.NoError(t, writeCredential(cfg.WorkerCredentialFile, "virtual-worker-credential"))
	newRunner := func() *Runner {
		return &Runner{runtimeExecutor: &runtimeExecutor{cfg: cfg, client: workerprotocol.NewClient(server.URL, "", time.Second)}}
	}
	runner := newRunner()
	require.NoError(t, runner.Authenticate(context.Background()))
	require.Equal(t, id.String(), runner.WorkerID())
	data, err := os.ReadFile(runner.identityPath())
	require.NoError(t, err)
	require.NotContains(t, string(data), "virtual-worker-credential")
	server.Close()
	runner = newRunner()
	require.NoError(t, runner.Authenticate(context.Background()))
	require.Equal(t, id.String(), runner.WorkerID())
	require.NoError(t, runner.InitializeOfflineIdentity())
	require.Equal(t, int32(1), calls.Load())

	require.NoError(t, writeCredential(cfg.WorkerCredentialFile, "rotated-credential"))
	require.Error(t, newRunner().Authenticate(context.Background()), "凭据变更后不能复用旧身份")
	require.Error(t, newRunner().InitializeOfflineIdentity())
}

func TestOfflineWorkerIdentityIsStableAndRejectsCorruption(t *testing.T) {
	cfg := config.Config{WorkerDataRoot: t.TempDir()}
	cfg.WorkerCredentialFile = filepath.Join(cfg.WorkerDataRoot, "credential")
	first := &Runner{runtimeExecutor: &runtimeExecutor{cfg: cfg}}
	require.NoError(t, first.InitializeOfflineIdentity())
	require.NotEqual(t, uuid.Nil.String(), first.WorkerID())
	second := &Runner{runtimeExecutor: &runtimeExecutor{cfg: cfg}}
	require.NoError(t, second.InitializeOfflineIdentity())
	require.Equal(t, first.WorkerID(), second.WorkerID())
	require.NoError(t, os.WriteFile(first.identityPath(), []byte("broken"), 0o600))
	require.Error(t, second.InitializeOfflineIdentity(), "损坏缓存不得悄悄生成另一机器身份")
}

func TestEnrollmentPersistsControlIdentity(t *testing.T) {
	id := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/worker/v1/enroll", r.URL.Path)
		_ = json.NewEncoder(w).Encode(workerprotocol.EnrollResponse{WorkerID: id,
			Credential: "virtual-enrolled-credential", ProtocolVersion: workerprotocol.Version})
	}))
	defer server.Close()
	cfg := config.Config{WorkerDataRoot: t.TempDir(), WorkerEnrollmentToken: "virtual-enrollment",
		WorkerControlURL: server.URL, WorkerProtocolVersion: workerprotocol.Version}
	cfg.WorkerCredentialFile = filepath.Join(cfg.WorkerDataRoot, "credential")
	runner := &Runner{runtimeExecutor: &runtimeExecutor{cfg: cfg, client: workerprotocol.NewClient(server.URL, "", time.Second)}}
	require.NoError(t, runner.Authenticate(context.Background()))
	require.Equal(t, id.String(), runner.WorkerID())
	require.NoError(t, runner.InitializeOfflineIdentity())
}
