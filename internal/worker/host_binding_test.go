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
	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestBrowserScopePersistsAndIsolatesWorkers(t *testing.T) {
	root := t.TempDir()
	first, err := LoadBrowserScope(root)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, first)
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() { id, err := LoadBrowserScope(root); require.NoError(t, err); require.Equal(t, first, id) })
	}
	group.Wait()
	info, err := os.Stat(filepath.Join(root, "browser-scope"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	other, err := LoadBrowserScope(t.TempDir())
	require.NoError(t, err)
	require.NotEqual(t, first, other)
	require.NoError(t, os.WriteFile(filepath.Join(root, "browser-scope"), []byte("corrupt"), 0o600))
	_, err = LoadBrowserScope(root)
	require.Error(t, err)
}

func TestHostBindingSnapshotsAndLocalToolBoundary(t *testing.T) {
	p := &Processor{cfg: config.Config{BrowserMCPURL: "http://localhost:8931/mcp"}}
	c := NewHostDesktopController(p, nil)
	ctx := context.Background()
	call := appserverhub.Call{Role: appserverhub.RoleDesktop, Method: "thread/start", Params: json.RawMessage(`{"cwd":"/tmp"}`)}
	plan, err := c.PrepareCall(ctx, call)
	require.NoError(t, err)
	require.Contains(t, string(plan.Params), `"chrome"`)
	require.Contains(t, string(plan.Params), `"browser_files"`)
	require.Contains(t, string(plan.Params), `"git"`)
	ephemeral, err := c.ConfigureEphemeralThread(ctx, call)
	require.NoError(t, err)
	require.NotContains(t, string(ephemeral), "dynamicTools")
	require.NotContains(t, string(ephemeral), "chrome")
	manifest := &workerprotocol.WorkspaceManifest{WorkspaceID: uuid.New(), OwnerParticipant: &workerprotocol.ParticipantIdentity{ParticipantID: uuid.New(), DisplayName: "第一位负责人"}}
	c.setBinding(manifest)
	first, _ := c.snapshot()
	c.active["existing-thread"] = &hostCallState{controller: first}
	next := *manifest
	next.OwnerParticipant = &workerprotocol.ParticipantIdentity{ParticipantID: uuid.New(), DisplayName: "第二位负责人"}
	c.setBinding(&next)
	current, _ := c.snapshot()
	require.NotSame(t, first, current)
	require.False(t, first.controlEnabled())
	require.True(t, current.controlEnabled())
	owner, ok := first.workspace.ownerParticipant()
	require.True(t, ok)
	require.Equal(t, "第一位负责人", owner.DisplayName)
	steer := appserverhub.Call{Role: appserverhub.RoleDesktop, Method: "turn/steer", Params: json.RawMessage(`{"threadId":"existing-thread","input":[]}`)}
	plan, err = c.PrepareCall(ctx, steer)
	require.NoError(t, err)
	require.Same(t, first, plan.State.(*hostCallState).controller)
	c.setBinding(nil)
	require.False(t, current.controlEnabled())
	// 未绑定时的旧 turn 不会因中途绑定而取得新身份。
	c.active["local-thread"] = &hostCallState{}
	c.setBinding(&next)
	steer.Params = json.RawMessage(`{"threadId":"local-thread","input":[]}`)
	plan, err = c.PrepareCall(ctx, steer)
	require.NoError(t, err)
	require.Nil(t, plan.State.(*hostCallState).controller)
	namespace := "tyrs_hand"
	result, err := p.handleLocalHostTool(ctx, hostWorkspaceRuntime{}, codex.ToolCallRequest{ThreadID: "thread", TurnID: "turn", CallID: "call", Namespace: &namespace, Tool: "automation_update"})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.ContentItems[0].Text, "需要有效的 Workspace 绑定")
	namespace = "git"
	result, err = p.handleLocalHostTool(ctx, hostWorkspaceRuntime{}, codex.ToolCallRequest{ThreadID: "thread", TurnID: "turn", CallID: "call", Namespace: &namespace, Tool: "publish_branch"})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.ContentItems[0].Text, "需要有效的 Workspace 绑定")
}

func TestHostBindingOutagePreservesSnapshotAndUnbindClearsCache(t *testing.T) {
	var mu sync.Mutex
	offline := true
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if offline {
			http.Error(w, "offline", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workspace":null}`))
	}))
	defer endpoint.Close()
	root := t.TempDir()
	p := &Processor{cfg: config.Config{WorkerDataRoot: root, ControlTimeout: time.Second}, logger: zap.NewNop(), client: workerprotocol.NewClient(endpoint.URL, "test", time.Second)}
	manifest := &workerprotocol.WorkspaceManifest{WorkspaceID: uuid.New()}
	require.NoError(t, SaveWorkspaceManifest(root, manifest))
	c := NewHostDesktopController(p, manifest)
	first, _ := c.snapshot()
	require.Error(t, c.syncHostEnvironment(context.Background()))
	current, _ := c.snapshot()
	require.Same(t, first, current)
	mu.Lock()
	offline = false
	mu.Unlock()
	require.NoError(t, c.syncHostEnvironment(context.Background()))
	current, _ = c.snapshot()
	require.Nil(t, current)
	_, err := LoadCachedWorkspaceManifest(root)
	require.ErrorIs(t, err, os.ErrNotExist)
}
