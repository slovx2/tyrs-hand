package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestThreadRegistrationRecoveryWithoutActiveTurn(t *testing.T) {
	for _, oldBarrier := range []bool{false, true} {
		t.Run(map[bool]string{false: "process-restart", true: "workspace-rebound"}[oldBarrier], func(t *testing.T) {
			root, workspaceID, requestID := t.TempDir(), uuid.New(), uuid.New()
			store, err := newJournalStore(root)
			require.NoError(t, err)
			entry := threadRegistrationJournal{Engine: "claude-code",
				Request: workerprotocol.DesktopThreadPrepareRequest{WorkspaceID: workspaceID, Operation: "fork",
					RequestKey: "original-request", Params: json.RawMessage(`{"threadId":"parent"}`)},
				Response: json.RawMessage(`{"thread":{"id":"forked-native-thread"}}`)}
			require.NoError(t, store.saveThread(entry))
			file, err := os.Stat(store.threadPath(workspaceID, "forked-native-thread"))
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o600), file.Mode().Perm())
			store, err = newJournalStore(root)
			require.NoError(t, err)
			var mu sync.Mutex
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				paths = append(paths, req.URL.Path)
				if req.Header.Get(workerprotocol.EngineHeader) != "claude-code" {
					t.Error("补登记未使用原引擎")
				}
				switch req.URL.Path {
				case "/worker/v1/desktop-thread-requests":
					var actual workerprotocol.DesktopThreadPrepareRequest
					if err := json.NewDecoder(req.Body).Decode(&actual); err != nil || actual.RequestKey != entry.Request.RequestKey ||
						actual.WorkspaceID != workspaceID || actual.Operation != "fork" {
						t.Error("恢复改变了原始会话请求身份")
					}
				case "/worker/v1/desktop-thread-requests/" + requestID.String() + "/complete":
					var actual workerprotocol.DesktopThreadCompleteRequest
					if err := json.NewDecoder(req.Body).Decode(&actual); err != nil || actual.WorkspaceID != workspaceID ||
						string(actual.Response) != string(entry.Response) {
						t.Error("恢复没有沿用已创建的原生会话")
					}
				default:
					t.Error("恢复调用了映射登记之外的接口")
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(workerprotocol.DesktopThreadState{ID: requestID})
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, err := workerprotocol.NewClient(server.URL, "test", time.Second).ForEngine("claude-code")
			require.NoError(t, err)
			p := &Processor{cfg: config.Config{ControlTimeout: time.Second}, client: client, journals: store,
				logger: zap.NewNop(), workspaces: &workspaceCodexRegistry{ctx: ctx}}
			if oldBarrier {
				previous := &desktopThreadRegistration{done: make(chan struct{}), err: errDesktopBindingChanged}
				close(previous.done)
				p.threadSync = map[string]*desktopThreadRegistration{workspaceID.String() + ":forked-native-thread": previous}
			}
			c := &desktopController{processor: p, workspace: &workspaceCodex{runtime: workspaceRuntime{WorkspaceID: workspaceID}}}
			// 故意不配置原生 runtime 或模型客户端；恢复只能重放幂等的 Control 映射登记。
			require.NoError(t, c.recoverThreadRegistrations())
			require.NoError(t, c.waitThreadRegistration(ctx, json.RawMessage(`{"threadId":"forked-native-thread"}`)))
			entries, err := store.loadThreads()
			require.NoError(t, err)
			require.Empty(t, entries)
			runs, err := store.loadAll()
			require.NoError(t, err)
			require.Empty(t, runs, "未创建 Turn 的会话也能独立恢复")
			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, []string{"/worker/v1/desktop-thread-requests",
				"/worker/v1/desktop-thread-requests/" + requestID.String() + "/complete"}, paths)
		})
	}
}

func TestThreadRegistrationRejectsInvalidIdentity(t *testing.T) {
	store, err := newJournalStore(t.TempDir())
	require.NoError(t, err)
	entry := threadRegistrationJournal{Engine: "claude-code",
		Request:  workerprotocol.DesktopThreadPrepareRequest{WorkspaceID: uuid.New(), Operation: "start", RequestKey: "original"},
		Response: json.RawMessage(`{"thread":{"id":"native"}}`)}
	require.NoError(t, store.saveThread(entry))
	path := store.threadPath(entry.Request.WorkspaceID, "native")
	wrongPath := filepath.Join(store.threadDirectory(), "different-identity.json")
	require.NoError(t, os.Rename(path, wrongPath))
	_, err = store.loadThreads()
	require.ErrorIs(t, err, errInvalidThreadRegistration)
	require.False(t, retryableControlError(err), "损坏身份不能被当作网络故障无限重试")
	require.NoError(t, os.Rename(wrongPath, path))
	client := workerprotocol.NewClient("http://127.0.0.1:1", "test", time.Second)
	err = syncThreadRegistration(t.Context(), client, time.Second, entry)
	require.ErrorIs(t, err, errInvalidThreadRegistration, "Codex 入口不能恢复 Claude 登记")
	entry.Request.Operation = "unknown"
	require.ErrorIs(t, store.saveThread(entry), errInvalidThreadRegistration)
}
