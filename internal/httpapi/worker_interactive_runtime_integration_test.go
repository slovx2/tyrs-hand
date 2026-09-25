//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/interactiveprotocol"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func (f sessionRuntimeFixture) startRun(t *testing.T, engine runtimeidentity.Engine) *workerprotocol.Task {
	t.Helper()
	ctx := t.Context()
	state := f.createThread(t, engine, "a", "same-thread")
	var sessionID, workerID uuid.UUID
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT session_id,worker_id FROM codex_thread_controls WHERE id=$1`, state.ControlID).Scan(&sessionID, &workerID))
	repo := codexcontrol.NewRepository(f.db, time.Minute)
	tx, err := f.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	intentID, _, err := repo.Enqueue(ctx, tx, codexcontrol.EnqueueRequest{
		SourceType: codexcontrol.SourceWorkspace, SessionID: sessionID, InputSurface: "client",
		IdempotencyKey: "runtime-input-" + string(engine), Instruction: "本地测试", Behavior: "start_when_idle", ReplyPolicy: "silent",
	})
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	runID := uuid.New()
	require.NoError(t, repo.StartWorkerInput(ctx, workerID, intentID, runID, engine))
	return &workerprotocol.Task{Claimed: codexcontrol.ClaimedControl{Intent: codexcontrol.Intent{
		ID: intentID, ControlID: state.ControlID, SessionID: sessionID,
	}, RunID: runID}}
}

func TestWorkerNativeApprovalsPersistIdentityAndArbitrateAnswers(t *testing.T) {
	f := newSessionRuntimeFixture(t)
	ctx := t.Context()
	states := map[runtimeidentity.Engine][]workerprotocol.InteractiveState{}
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		task := f.startRun(t, engine)
		client := f.clients[engine]
		for index, method := range []string{interactiveprotocol.CommandApproval, interactiveprotocol.FileApproval} {
			params := json.RawMessage(`{"threadId":"same-thread","turnId":"same-turn","itemId":"same-item","command":"printf approved","reason":"等待明确许可"}`)
			id, _ := json.Marshal(index + 1)
			state, err := client.RegisterInteractive(ctx, task, method, id, params, 1)
			require.NoError(t, err)
			require.Equal(t, method, state.Method)
			require.JSONEq(t, string(id), string(state.RequestID))
			require.EqualValues(t, 1, state.AppServerGeneration)
			require.Contains(t, string(state.Questions), "允许本次")
			require.Nil(t, state.DeadlineAt, "审批不能自动允许")
			states[engine] = append(states[engine], state)
			replay, err := client.RegisterInteractive(ctx, task, method, id, params, 1)
			require.NoError(t, err)
			require.Equal(t, state.ID, replay.ID)
			_, err = client.RegisterInteractive(ctx, task, method, id, params, 2)
			assertRuntimeHTTPStatus(t, err, http.StatusConflict)
			answer := workerprotocol.InteractiveAnswerRequest{WorkspaceID: f.workspaceID, ThreadID: "same-thread",
				TurnID: "same-turn", ItemID: "same-item", Surface: "discord", RequestID: id, AppServerGeneration: 2,
				Answer: json.RawMessage(`{"answers":{"approval":{"answers":["允许本次"]}}}`)}
			_, err = client.AnswerInteractive(ctx, answer)
			assertRuntimeHTTPStatus(t, err, http.StatusNotFound)
			answer.AppServerGeneration = 1
			accepted, err := client.AnswerInteractive(ctx, answer)
			require.NoError(t, err)
			require.True(t, accepted.Accepted)
			require.JSONEq(t, `{"decision":"accept"}`, string(accepted.Answer))
			answer.Surface, answer.Answer = "desktop", json.RawMessage(`{"decision":"decline"}`)
			duplicate, err := client.AnswerInteractive(ctx, answer)
			require.NoError(t, err)
			require.False(t, duplicate.Accepted)
			require.JSONEq(t, string(accepted.Answer), string(duplicate.Answer))
		}
		require.NotEqual(t, states[engine][0].ID, states[engine][1].ID, "同一条目的原生回调必须单独记录")
	}
	require.NotEqual(t, states[runtimeidentity.Codex][0].ID, states[runtimeidentity.Claude][0].ID)
	_, err := f.clients[runtimeidentity.Codex].InteractiveState(ctx, states[runtimeidentity.Claude][0].ID)
	assertRuntimeHTTPStatus(t, err, http.StatusNotFound)
}

func TestWorkerInteractiveRuntimeIsolationAndArbitration(t *testing.T) {
	f := newSessionRuntimeFixture(t)
	ctx := t.Context()
	params := json.RawMessage(`{"threadId":"same-thread","turnId":"same-turn","itemId":"same-item","questions":[{"id":"q","header":"选择","question":"继续吗？"}]}`)
	states := map[runtimeidentity.Engine]workerprotocol.InteractiveState{}
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		task := f.startRun(t, engine)
		client := f.clients[engine]
		state, err := client.RegisterInteractive(ctx, task, "item/tool/requestUserInput", json.RawMessage(`1`), params, 1)
		require.NoError(t, err)
		states[engine] = state
		replay, err := client.RegisterInteractive(ctx, task, "item/tool/requestUserInput", json.RawMessage(`1`), params, 1)
		require.NoError(t, err)
		require.Equal(t, state.ID, replay.ID)
		_, err = client.RegisterInteractive(ctx, task, "item/tool/requestUserInput", json.RawMessage(`1`), params, 2)
		assertRuntimeHTTPStatus(t, err, http.StatusConflict)
		_, err = client.RegisterInteractive(ctx, task, "item/tool/requestUserInput", json.RawMessage(`2`), params, 1)
		assertRuntimeHTTPStatus(t, err, http.StatusConflict)
	}
	require.NotEqual(t, states[runtimeidentity.Codex].ID, states[runtimeidentity.Claude].ID)
	_, err := f.clients[runtimeidentity.Codex].InteractiveState(ctx, states[runtimeidentity.Claude].ID)
	assertRuntimeHTTPStatus(t, err, http.StatusNotFound)
	_, err = f.clients[runtimeidentity.Claude].InteractiveState(ctx, states[runtimeidentity.Codex].ID)
	assertRuntimeHTTPStatus(t, err, http.StatusNotFound)

	// 两个端同时回答同一 Claude 提问，只有一个答案生效。
	answers := make([]workerprotocol.InteractiveState, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, surface := range []string{"desktop", "discord"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answers[i], errs[i] = f.clients[runtimeidentity.Claude].AnswerInteractive(ctx,
				workerprotocol.InteractiveAnswerRequest{WorkspaceID: f.workspaceID,
					RequestID: json.RawMessage(`1`), AppServerGeneration: 1,
					ThreadID: "same-thread", TurnID: "same-turn", ItemID: "same-item", Surface: surface,
					Answer: json.RawMessage(`{"answers":{"q":{"answers":["` + surface + `"]}}}`)})
		}()
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	require.NotEqual(t, answers[0].Accepted, answers[1].Accepted)
	require.JSONEq(t, string(answers[0].Answer), string(answers[1].Answer))
	codex, err := f.clients[runtimeidentity.Codex].InteractiveState(ctx, states[runtimeidentity.Codex].ID)
	require.NoError(t, err)
	require.Equal(t, "pending", codex.Status)
	require.Empty(t, codex.Answer)
}
