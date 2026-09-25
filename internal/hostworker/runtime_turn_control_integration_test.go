//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

type runtimeTurnGate struct {
	entered, release chan struct{}
	once             sync.Once
}

func newRuntimeTurnGate() *runtimeTurnGate {
	return &runtimeTurnGate{entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *runtimeTurnGate) unblock() { g.once.Do(func() { close(g.release) }) }

type runtimeEngineTurnFixture struct {
	steer, interrupt *runtimeTurnGate
	calls            atomic.Int64
}

type runtimeTurnControlFixture struct {
	engines map[runtimeidentity.Engine]*runtimeEngineTurnFixture
}

func newRuntimeTurnControlFixture() *runtimeTurnControlFixture {
	f := &runtimeTurnControlFixture{engines: map[runtimeidentity.Engine]*runtimeEngineTurnFixture{}}
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		f.engines[engine] = &runtimeEngineTurnFixture{steer: newRuntimeTurnGate(), interrupt: newRuntimeTurnGate()}
	}
	return f
}

func (f *runtimeTurnControlFixture) unblock() {
	for _, engine := range f.engines {
		engine.steer.unblock()
		engine.interrupt.unblock()
	}
}

func (f *runtimeTurnControlFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request,
	engine runtimeidentity.Engine, body []byte,
) {
	fixture := f.engines[engine]
	call := fixture.calls.Add(1)
	text := ""
	var gate *runtimeTurnGate
	switch call {
	case 1:
		require.Contains(t, string(body), "SSH_ORIGINAL_INPUT")
		text, gate = "BEFORE_STEER", fixture.steer
	case 2:
		require.Contains(t, string(body), "SSH_ORIGINAL_INPUT")
		require.Contains(t, string(body), "SSH_STEER_INPUT", "追加输入必须穿过 SSH、Hub 和真实 SDK")
		require.Equal(t, 1, strings.Count(string(body), "SSH_STEER_INPUT"))
		text = "AFTER_STEER"
	case 3:
		require.Contains(t, string(body), "SSH_INTERRUPT_INPUT")
		text, gate = "CANCELLED_RESULT", fixture.interrupt
	case 4:
		require.Contains(t, string(body), "SSH_AFTER_RESTART")
		require.Contains(t, string(body), "SSH_STEER_INPUT", "重启恢复必须保持已确认上下文")
		text = "AFTER_RESTART"
	default:
		t.Errorf("%s 不允许未提交的模型请求 %d", engine, call)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if gate != nil {
		close(gate.entered)
		select {
		case <-gate.release:
		case <-request.Context().Done():
			if call != 3 {
				t.Errorf("%s 追加输入前不应取消模型请求", engine)
			}
			return
		}
	}
	runtimeTextModel(w, request, text, fmt.Sprintf("%s-%d", engine, call))
}

// SUBMIT-004 / EVENTS-005：使用两个真实客户端穿过 SSH，验证输入和取消，不由伪造历史代替。
func verifyRuntimeTurnControl(t *testing.T, ctx context.Context, registry *RuntimeRegistry,
	connection *ssh.Client, client *codex.SocketClient, engine runtimeidentity.Engine, fixture *runtimeEngineTurnFixture,
) {
	t.Helper()
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{
		"approvalPolicy": "never", "sandbox": "danger-full-access",
	})
	second := connectRuntimeSSH(t, ctx, connection, engine)
	subscription := client.Subscribe(codex.ThreadFilter{ThreadID: thread.ID})
	defer subscription.Close()
	start := func(text, messageID string) string {
		var result struct{ Turn struct{ ID string } }
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": thread.ID,
			"clientUserMessageId": messageID, "input": []map[string]any{{"type": "text", "text": text}}}, &result))
		return result.Turn.ID
	}
	waitGate := func(gate *runtimeTurnGate) {
		select {
		case <-gate.entered:
		case <-ctx.Done():
			t.Fatal("模型请求未到达脚本化等待点")
		}
	}
	terminalCounts := map[string]int{}
	waitTerminal := func(id, status string) {
		for {
			select {
			case <-ctx.Done():
				t.Fatal("真实 SSH 未收到回合终态")
			case event, ok := <-subscription.Events():
				require.True(t, ok)
				if event.Method != "turn/completed" {
					continue
				}
				var params struct {
					ThreadID string
					Turn     struct{ ID, Status string }
				}
				require.NoError(t, json.Unmarshal(event.Params, &params))
				require.Equal(t, thread.ID, params.ThreadID)
				terminalCounts[params.Turn.ID]++
				require.Equal(t, 1, terminalCounts[params.Turn.ID], "不能发送重复终态")
				require.Equal(t, id, params.Turn.ID)
				require.Equal(t, status, params.Turn.Status)
				return
			}
		}
	}
	turnID := start("SSH_ORIGINAL_INPUT", "ssh-original")
	waitGate(fixture.steer)
	readSessionThread(t, ctx, second, "thread/resume", map[string]any{"threadId": thread.ID})
	var steered struct{ TurnID string }
	steer := map[string]any{"threadId": thread.ID, "expectedTurnId": turnID,
		"clientUserMessageId": "ssh-steer", "input": []map[string]any{{"type": "text", "text": "SSH_STEER_INPUT"}}}
	require.NoError(t, second.Call(ctx, "turn/steer", steer, &steered))
	require.Equal(t, turnID, steered.TurnID)
	fixture.steer.unblock()
	waitTerminal(turnID, "completed")
	require.Equal(t, int64(2), fixture.calls.Load())
	history := readSessionThread(t, ctx, client, "thread/read", map[string]any{"threadId": thread.ID, "includeTurns": true})
	require.Len(t, history.Turns, 1, "追加输入不得创建第二个回合")
	interruptedID := start("SSH_INTERRUPT_INPUT", "ssh-interrupt")
	waitGate(fixture.interrupt)
	var ignored any
	require.Error(t, second.Call(ctx, "turn/interrupt", map[string]any{"threadId": thread.ID, "turnId": "wrong-turn"}, &ignored))
	require.NoError(t, second.Call(ctx, "turn/interrupt", map[string]any{"threadId": thread.ID, "turnId": interruptedID}, &ignored))
	waitTerminal(interruptedID, "interrupted")
	fixture.interrupt.unblock()
	history = readSessionThread(t, ctx, client, "thread/read", map[string]any{"threadId": thread.ID, "includeTurns": true})
	require.Len(t, history.Turns, 2)
	require.Equal(t, "interrupted", history.Turns[1].Status)
	other := runtimeidentity.Codex
	if engine == runtimeidentity.Codex {
		other = runtimeidentity.Claude
	}
	generation := registry.entries[other].Runtime.Generation()
	require.NoError(t, registry.Restart(engine))
	require.Equal(t, generation, registry.entries[other].Runtime.Generation())
	client = connectRuntimeSSH(t, ctx, connection, engine)
	readSessionThread(t, ctx, client, "thread/resume", map[string]any{"threadId": thread.ID})
	subscription.Close()
	subscription = client.Subscribe(codex.ThreadFilter{ThreadID: thread.ID})
	defer subscription.Close()
	nextID := start("SSH_AFTER_RESTART", "ssh-after-restart")
	waitTerminal(nextID, "completed")
	history = readSessionThread(t, ctx, client, "thread/read", map[string]any{"threadId": thread.ID, "includeTurns": true})
	require.Len(t, history.Turns, 3)
	require.Equal(t, "interrupted", history.Turns[1].Status)
	require.Equal(t, int64(4), fixture.calls.Load())
}
