//go:build integration

package bootstrap

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

// 真正终止原生进程并重建 Hub；旧回答不能转交给新 generation。
func verifyControlInteractionRestart(t *testing.T, ctx context.Context, f controlRuntimeFixture,
	app *WorkerApp, engine runtimeidentity.Engine, id uuid.UUID, request codex.ServerRequest, answer json.RawMessage,
) map[string]any {
	t.Helper()
	entry, err := app.Runtimes.Entry(engine)
	require.NoError(t, err)
	other := runtimeidentity.Codex
	if engine == other {
		other = runtimeidentity.Claude
	}
	otherEntry, err := app.Runtimes.Entry(other)
	require.NoError(t, err)
	before, otherBefore := entry.Runtime.Generation(), otherEntry.Runtime.Generation()
	var identity workerprotocol.InteractiveAnswerRequest
	identity.RequestID, identity.AppServerGeneration = request.ID, before
	identity.Surface, identity.Answer = "desktop", answer
	require.NoError(t, f.db.QueryRowContext(ctx, `SELECT c.workspace_id,q.thread_id,q.turn_id,q.item_id
		FROM codex_interactive_requests q JOIN codex_thread_controls c ON c.id=q.control_id WHERE q.id=$1`, id).
		Scan(&identity.WorkspaceID, &identity.ThreadID, &identity.TurnID, &identity.ItemID))
	require.NoError(t, app.Runtimes.Restart(engine))
	require.NotEqual(t, before, entry.Runtime.Generation())
	require.Equal(t, otherBefore, otherEntry.Runtime.Generation())
	require.Eventually(t, func() bool {
		var status string
		return f.db.QueryRowContext(ctx, "SELECT status FROM codex_interactive_requests WHERE id=$1", id).Scan(&status) == nil && status == "interrupted"
	}, 15*time.Second, 25*time.Millisecond, "原生进程结束必须使 Control 旧交互失效")
	credential, err := os.ReadFile(f.cfg.WorkerCredentialFile)
	require.NoError(t, err)
	control, err := workerprotocol.NewClient(f.cfg.WorkerControlURL, string(credential), 5*time.Second).ForEngine(engine)
	require.NoError(t, err)
	late, err := control.AnswerInteractive(ctx, identity)
	require.NoError(t, err)
	require.False(t, late.Accepted)
	require.False(t, late.Ready)
	require.Equal(t, "interrupted", late.Status)
	require.Empty(t, late.Answer)
	identity.AppServerGeneration = entry.Runtime.Generation()
	_, err = control.AnswerInteractive(ctx, identity)
	var httpErr *workerprotocol.HTTPError
	require.ErrorAs(t, err, &httpErr)
	require.Equal(t, http.StatusNotFound, httpErr.StatusCode)
	lateDiscord, err := discordintegration.NewManager(f.db, nil).AnswerInteractive(ctx, "protocol", id, 0, 0, "")
	require.NoError(t, err)
	require.False(t, lateDiscord.Complete)
	client, _ := connectBootstrapSSH(t, ctx, entry, f.signer)
	require.NoError(t, client.Call(ctx, "config/read", map[string]any{}, nil))
	return map[string]any{"restarted": true, "oldAnswerAccepted": late.Accepted,
		"oldGeneration": before, "newGeneration": entry.Runtime.Generation(), "otherEngineUnchanged": true}
}
