//go:build integration

package hostworker

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

type runtimeCrossEntryFixture struct {
	calls      map[runtimeidentity.Engine]*atomic.Int64
	background atomic.Int64
}

func newRuntimeCrossEntryFixture() *runtimeCrossEntryFixture {
	return &runtimeCrossEntryFixture{calls: map[runtimeidentity.Engine]*atomic.Int64{
		runtimeidentity.Codex: {}, runtimeidentity.Claude: {}}}
}

func (f *runtimeCrossEntryFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, engine runtimeidentity.Engine, body []byte) {
	own, other := "ISO002_"+string(engine), "ISO002_"+string(runtimeidentity.Codex)
	if engine == runtimeidentity.Codex {
		other = "ISO002_" + string(runtimeidentity.Claude)
	}
	require.NotContains(t, string(body), other, "另一入口的会话内容不能进入本引擎模型")
	if !strings.Contains(string(body), own) {
		// 原生后台标题等请求不属于业务回合，但同样不能携带另一引擎的内容。
		f.background.Add(1)
		runtimeTextModel(w, request, "ISO002_BACKGROUND", "iso002-background")
		return
	}
	f.calls[engine].Add(1)
	runtimeTextModel(w, request, "ISO002_DONE_"+string(engine), "iso002-"+string(engine))
}

type crossEntryThread struct {
	ID, Name string
	Turns    []struct{ ID, Status string }
}

// ISOLATION-002：同一 Worker 内两入口的会话同名、ID 互投时，读取、恢复、改名、归档、分叉和提交均被本入口拒绝；
// 被越界操作的原会话状态、同名会话缓存与模型调用均不受影响。
func verifyRuntimeCrossEntryIsolation(t *testing.T, ctx context.Context, connections map[runtimeidentity.Engine]*ssh.Client, root string, fixture *runtimeCrossEntryFixture) {
	t.Helper()
	engines := []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude}
	clients := map[runtimeidentity.Engine]*codex.SocketClient{}
	threads := map[runtimeidentity.Engine]string{}
	read := func(engine runtimeidentity.Engine, threadID string) crossEntryThread {
		var result struct{ Thread crossEntryThread }
		require.NoError(t, clients[engine].Call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true}, &result))
		return result.Thread
	}
	for _, engine := range engines {
		clients[engine] = connectRuntimeSSH(t, ctx, connections[engine], engine)
		thread := readSessionThread(t, ctx, clients[engine], "thread/start", map[string]any{
			"cwd": root, "approvalPolicy": "never", "sandbox": "read-only"})
		threads[engine] = thread.ID
		var started struct{ Turn struct{ ID string } }
		require.NoError(t, clients[engine].Call(ctx, "turn/start", map[string]any{"threadId": thread.ID,
			"input": []map[string]string{{"type": "text", "text": "ISO002_" + string(engine)}}}, &started))
		waitSessionTurn(t, ctx, clients[engine], thread.ID, started.Turn.ID)
		// 两入口强制使用同一会话名称，名称缓存必须仍按引擎隔离。
		require.NoError(t, clients[engine].Call(ctx, "thread/name/set", map[string]any{"threadId": thread.ID, "name": "iso002-same-name"}, nil))
	}
	require.NotEqual(t, threads[runtimeidentity.Codex], threads[runtimeidentity.Claude])
	baseline := map[runtimeidentity.Engine]crossEntryThread{}
	for _, engine := range engines {
		baseline[engine] = read(engine, threads[engine])
		require.Equal(t, "iso002-same-name", baseline[engine].Name)
		require.Len(t, baseline[engine].Turns, 1)
	}
	calls := map[runtimeidentity.Engine]int64{}
	for _, engine := range engines {
		calls[engine] = fixture.calls[engine].Load()
	}
	for _, engine := range engines {
		foreign := threads[runtimeidentity.Claude]
		if engine == runtimeidentity.Claude {
			foreign = threads[runtimeidentity.Codex]
		}
		for method, params := range map[string]map[string]any{
			"thread/read":     {"threadId": foreign, "includeTurns": true},
			"thread/resume":   {"threadId": foreign},
			"thread/name/set": {"threadId": foreign, "name": "iso002-hijacked"},
			"thread/archive":  {"threadId": foreign},
			"thread/fork":     {"threadId": foreign},
			"turn/start": {"threadId": foreign,
				"input": []map[string]string{{"type": "text", "text": "ISO002_" + string(engine) + "_FOREIGN"}}},
		} {
			var ignored any
			require.Error(t, clients[engine].Call(ctx, method, params, &ignored),
				"%s 入口不能对另一引擎会话执行 %s", engine, method)
		}
	}
	for _, engine := range engines {
		require.Equal(t, calls[engine], fixture.calls[engine].Load(), "越界操作不能触发任何引擎的模型调用")
		current := read(engine, threads[engine])
		require.Equal(t, baseline[engine], current, "越界操作不能改变原会话的名称、回合或状态")
		var listed struct{ Data []struct{ ID string } }
		require.NoError(t, clients[engine].Call(ctx, "thread/list", map[string]any{}, &listed))
		ids := map[string]bool{}
		for _, item := range listed.Data {
			ids[item.ID] = true
		}
		require.True(t, ids[threads[engine]], "%s 列表必须包含本入口会话", engine)
		for _, other := range engines {
			if other != engine {
				require.False(t, ids[threads[other]], "%s 列表不能出现另一引擎会话", engine)
			}
		}
	}
	// 同名会话各自改名，不能经共享名称缓存影响另一入口。
	require.NoError(t, clients[runtimeidentity.Codex].Call(ctx, "thread/name/set",
		map[string]any{"threadId": threads[runtimeidentity.Codex], "name": "iso002-codex-renamed"}, nil))
	require.Equal(t, "iso002-codex-renamed", read(runtimeidentity.Codex, threads[runtimeidentity.Codex]).Name)
	require.Equal(t, "iso002-same-name", read(runtimeidentity.Claude, threads[runtimeidentity.Claude]).Name)
	for _, engine := range engines {
		require.Equal(t, int64(1), fixture.calls[engine].Load(), "每个引擎只执行一次显式业务回合")
	}
}
