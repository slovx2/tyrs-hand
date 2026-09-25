//go:build integration

package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestWorkerClaimPrioritizesActiveCommandsWithinRuntime(t *testing.T) {
	f := newSessionRuntimeFixture(t)
	repo := codexcontrol.NewRepository(f.db, time.Minute)
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		t.Run(string(engine), func(t *testing.T) {
			idle := f.createThread(t, engine, "1", "idle")
			active := f.createThread(t, engine, "2", "active")
			enqueue := func(controlID uuid.UUID, operation string) uuid.UUID {
				t.Helper()
				var sessionID uuid.UUID
				require.NoError(t, f.db.QueryRow(`SELECT session_id FROM codex_thread_controls WHERE id=$1`, controlID).Scan(&sessionID))
				tx, err := f.db.BeginTx(t.Context(), nil)
				require.NoError(t, err)
				id, _, err := repo.Enqueue(t.Context(), tx, codexcontrol.EnqueueRequest{SourceType: codexcontrol.SourceWorkspace,
					SessionID: sessionID, Operation: operation, InputSurface: "client", Instruction: "capacity-test", IdempotencyKey: uuid.NewString(), ReplyPolicy: "silent"})
				require.NoError(t, err)
				require.NoError(t, tx.Commit())
				return id
			}
			idleID := enqueue(idle.ControlID, "turn_input")
			steerID := enqueue(active.ControlID, "turn_input")
			stopID := enqueue(active.ControlID, "interrupt")
			client := f.clients[engine]
			claim, err := client.Claim(t.Context(), workerprotocol.ClaimRequest{Role: "discord", OnlyActive: true})
			require.NoError(t, err)
			require.Nil(t, claim.Task, "无活动会话时不能新建 Turn")
			request := workerprotocol.ClaimRequest{Role: "discord", OnlyActive: true, ActiveControlIDs: []uuid.UUID{active.ControlID}}
			claim, err = client.Claim(t.Context(), request)
			require.NoError(t, err)
			require.NotNil(t, claim.Task)
			require.Equal(t, stopID, claim.Task.Claimed.ID, "停止不能被较早的排队任务或 steer 堵住")
			require.Equal(t, engine, claim.Task.Snapshot.Runtime.Engine)
			_, err = f.db.Exec(`UPDATE codex_turn_intents SET resolved_action='interrupt' WHERE id=$1`, stopID)
			require.NoError(t, err)
			request.OnlyActive = false
			claim, err = client.Claim(t.Context(), request)
			require.NoError(t, err)
			require.Equal(t, steerID, claim.Task.Claimed.ID, "有空位时仍先处理活动会话")
			claim, err = client.Claim(t.Context(), workerprotocol.ClaimRequest{Role: "discord"})
			require.NoError(t, err)
			require.Equal(t, idleID, claim.Task.Claimed.ID, "没有活动提示时保持原队列顺序")
			other := runtimeidentity.Claude
			if engine == other {
				other = runtimeidentity.Codex
			}
			request.OnlyActive = true
			claim, err = f.clients[other].Claim(t.Context(), request)
			require.NoError(t, err)
			require.Nil(t, claim.Task, "活动 ID 列表不能突破引擎作用域")
			_, err = client.Claim(t.Context(), workerprotocol.ClaimRequest{Role: "discord", ActiveControlIDs: []uuid.UUID{uuid.Nil}})
			assertRuntimeHTTPStatus(t, err, http.StatusBadRequest)
		})
	}
}
