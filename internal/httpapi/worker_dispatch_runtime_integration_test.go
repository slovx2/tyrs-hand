//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestWorkerRuntimeClaimEventsAndTerminalIsolation(t *testing.T) {
	f := newSessionRuntimeFixture(t)
	ctx := t.Context()
	repo := codexcontrol.NewRepository(f.db, time.Minute)
	inputs := map[runtimeidentity.Engine]uuid.UUID{}
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		state := f.createThread(t, engine, "a", "same-thread")
		var sessionID uuid.UUID
		require.NoError(t, f.db.QueryRowContext(ctx, `SELECT session_id FROM codex_thread_controls WHERE id=$1`, state.ControlID).Scan(&sessionID))
		tx, err := f.db.BeginTx(ctx, nil)
		require.NoError(t, err)
		input, _, err := repo.Enqueue(ctx, tx, codexcontrol.EnqueueRequest{
			SourceType: codexcontrol.SourceWorkspace, SessionID: sessionID, InputSurface: "client",
			IdempotencyKey: "dispatch-" + string(engine), Instruction: "本地验收", Behavior: "start_when_idle", ReplyPolicy: "silent",
		})
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
		inputs[engine] = input
	}
	tasks := map[runtimeidentity.Engine]*workerprotocol.Task{}
	for engine, client := range f.clients {
		claim, err := client.Claim(ctx, workerprotocol.ClaimRequest{Role: "discord"})
		require.NoError(t, err)
		require.NotNil(t, claim.Task)
		require.Equal(t, engine, claim.Task.Snapshot.Runtime.Engine)
		require.Equal(t, inputs[engine], claim.Task.Claimed.ID)
		claim.Task.Claimed.RunID = uuid.New()
		tasks[engine] = claim.Task
		require.NoError(t, client.DecideInput(ctx, claim.Task, "start", ""))
		require.NoError(t, client.DecideInput(ctx, claim.Task, "start", ""))
		next, err := client.Claim(ctx, workerprotocol.ClaimRequest{Role: "discord"})
		require.NoError(t, err)
		require.Nil(t, next.Task)
		var count int
		require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_runs WHERE primary_intent_id=$1`, inputs[engine]).Scan(&count))
		require.Equal(t, 1, count)
	}
	for engine, task := range tasks {
		client := f.clients[engine]
		other := runtimeidentity.Codex
		if engine == other {
			other = runtimeidentity.Claude
		}
		events := []workerprotocol.EventInput{{Sequence: 1, Type: "item/completed",
			Payload: json.RawMessage(`{"threadId":"same-thread","turnId":"same-turn","item":{"id":"same-item","type":"agentMessage","text":"` + string(engine) + `"}}`)}}
		assertRuntimeHTTPStatus(t, f.clients[other].Events(ctx, task, events), http.StatusNotFound)
		require.NoError(t, client.Events(ctx, task, events))
		require.NoError(t, client.Events(ctx, task, events))
		result := codexcontrol.TurnResult{FinalAnswer: string(engine)}
		assertRuntimeHTTPStatus(t, f.clients[other].Complete(ctx, task, result), http.StatusNotFound)
		require.NoError(t, client.Complete(ctx, task, result))
		require.NoError(t, client.Complete(ctx, task, result))
		var count int
		var text, status string
		require.NoError(t, f.db.QueryRowContext(ctx, `SELECT count(*),min(payload->'item'->>'text') FROM agent_events WHERE run_id=$1 AND external_event_id='worker:1'`, task.Claimed.RunID).Scan(&count, &text))
		require.Equal(t, 1, count)
		require.Equal(t, string(engine), text)
		require.NoError(t, f.db.QueryRowContext(ctx, `SELECT status FROM codex_turn_runs WHERE id=$1`, task.Claimed.RunID).Scan(&status))
		require.Equal(t, "completed", status)
	}
}
