//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/interactiveprotocol"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestWorkerExtendedInteractiveRejectsWrongIdentityAndAnswers(t *testing.T) {
	f := newSessionRuntimeFixture(t)
	ctx := t.Context()
	for _, test := range []struct {
		engine                                     runtimeidentity.Engine
		method, params, invalid, valid, turn, item string
	}{
		{runtimeidentity.Codex, interactiveprotocol.PermissionApproval,
			`{"threadId":"same-thread","turnId":"permission-turn","itemId":"permission-item","permissions":{"fileSystem":{"read":null,"write":["/workspace"]}}}`,
			`{"permissions":{"network":{"enabled":true}},"scope":"session"}`,
			`{"permissions":{},"scope":"turn"}`, "permission-turn", "permission-item"},
		{runtimeidentity.Claude, interactiveprotocol.MCPElicitation,
			`{"threadId":"same-thread","turnId":null,"mode":"form","serverName":"fixture","message":"输入","requestedSchema":{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"]}}`,
			`{"action":"accept","content":{"value":"wrong"}}`,
			`{"action":"accept","content":{"value":42},"_meta":null}`, "", ""},
	} {
		t.Run(string(test.engine), func(t *testing.T) {
			task := f.startRun(t, test.engine)
			client := f.clients[test.engine]
			state, err := client.RegisterInteractive(ctx, task, test.method, json.RawMessage(`1`), json.RawMessage(test.params), 7)
			require.NoError(t, err)
			var storedTurn, storedItem string
			require.NoError(t, f.db.QueryRowContext(ctx, "SELECT turn_id,item_id FROM codex_interactive_requests WHERE id=$1", state.ID).Scan(&storedTurn, &storedItem))
			require.Equal(t, test.turn, storedTurn)
			require.Equal(t, test.item, storedItem)
			answer := workerprotocol.InteractiveAnswerRequest{WorkspaceID: f.workspaceID, ThreadID: "same-thread",
				TurnID: test.turn, ItemID: test.item, Surface: "desktop", RequestID: json.RawMessage(`1`),
				AppServerGeneration: 7, Answer: json.RawMessage(test.valid)}
			for _, wrong := range []struct {
				name   string
				mutate func(*workerprotocol.InteractiveAnswerRequest)
			}{
				{"workspace", func(a *workerprotocol.InteractiveAnswerRequest) { a.WorkspaceID = uuid.New() }},
				{"thread", func(a *workerprotocol.InteractiveAnswerRequest) { a.ThreadID = "another-thread" }},
				{"turn", func(a *workerprotocol.InteractiveAnswerRequest) { a.TurnID = "another-turn" }},
				{"item", func(a *workerprotocol.InteractiveAnswerRequest) { a.ItemID = "another-item" }},
				{"request", func(a *workerprotocol.InteractiveAnswerRequest) { a.RequestID = json.RawMessage(`2`) }},
				{"generation", func(a *workerprotocol.InteractiveAnswerRequest) { a.AppServerGeneration++ }},
			} {
				t.Run(wrong.name, func(t *testing.T) {
					changed := answer
					wrong.mutate(&changed)
					_, err := client.AnswerInteractive(ctx, changed)
					assertRuntimeHTTPStatus(t, err, http.StatusNotFound)
				})
			}
			other := runtimeidentity.Codex
			if test.engine == other {
				other = runtimeidentity.Claude
			}
			_, err = f.clients[other].AnswerInteractive(ctx, answer)
			assertRuntimeHTTPStatus(t, err, http.StatusNotFound)
			invalid := answer
			invalid.Answer = json.RawMessage(test.invalid)
			_, err = client.AnswerInteractive(ctx, invalid)
			assertRuntimeHTTPStatus(t, err, http.StatusBadRequest)
			pending, err := client.InteractiveState(ctx, state.ID)
			require.NoError(t, err)
			require.Equal(t, "pending", pending.Status)
			require.Empty(t, pending.Answer)
			accepted, err := client.AnswerInteractive(ctx, answer)
			require.NoError(t, err)
			require.True(t, accepted.Accepted)
			require.Equal(t, "resolved", accepted.Status)
		})
	}
}
