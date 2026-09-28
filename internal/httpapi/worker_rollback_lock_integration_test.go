//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestWorkerRollbackLocksParentsBeforeControl(t *testing.T) {
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		for _, parent := range []string{"session", "conversation"} {
			t.Run(string(engine)+"/"+parent, func(t *testing.T) {
				f := newSessionRuntimeFixture(t)
				ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
				defer cancel()
				task := f.startRun(t, engine)
				require.NoError(t, f.clients[engine].ConfirmTurn(ctx, task, "rollback-turn"))
				require.NoError(t, f.clients[engine].Complete(ctx, task, codexcontrol.TurnResult{
					TurnID: "rollback-turn", FinalAnswer: "completed",
				}))
				if parent == "conversation" {
					seedRollbackConversation(t, ctx, f, task)
				}
				tx, err := f.db.BeginTx(ctx, nil)
				require.NoError(t, err)
				defer func() { _ = tx.Rollback() }()
				var pid int
				var locked uuid.UUID
				require.NoError(t, tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid))
				if parent == "session" {
					require.NoError(t, tx.QueryRowContext(ctx, "SELECT id FROM workspace_sessions WHERE id=$1 FOR UPDATE", task.Claimed.SessionID).Scan(&locked))
				} else {
					require.NoError(t, tx.QueryRowContext(ctx, `SELECT conversation.id FROM discord_conversations conversation
						JOIN codex_thread_controls control ON control.discord_conversation_id=conversation.id
						WHERE control.id=$1 FOR UPDATE OF conversation`, task.Claimed.ControlID).Scan(&locked))
				}
				done := make(chan error, 1)
				go func() {
					_, err := f.clients[engine].PrepareDesktopRollback(ctx, workerprotocol.DesktopRollbackPrepareRequest{
						WorkspaceID: f.workspaceID, RequestKey: strings.Repeat("d", 64),
						Params: json.RawMessage(`{"threadId":"same-thread","beforeTurnId":"rollback-turn"}`),
					})
					done <- err
				}()
				require.Eventually(t, func() bool {
					var blocked bool
					return f.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))", pid).Scan(&blocked) == nil && blocked
				}, 3*time.Second, 10*time.Millisecond, "回退请求必须已到达真实父记录锁")
				require.NoError(t, tx.QueryRowContext(ctx, "SELECT id FROM codex_thread_controls WHERE id=$1 FOR UPDATE NOWAIT", task.Claimed.ControlID).Scan(&locked), "回退不能持有 Control 反向等待 metadata 的父记录锁")
				require.NoError(t, tx.Commit())
				select {
				case err := <-done:
					require.NoError(t, err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			})
		}
	}
}

// 内存 HTTP fixture 不运行 Discord 投递器；显式建立它完成投递后的真实外键关系。
func seedRollbackConversation(t *testing.T, ctx context.Context, f sessionRuntimeFixture, task *workerprotocol.Task) {
	t.Helper()
	var resourceID, forumID, conversationID uuid.UUID
	require.NoError(t, f.db.QueryRowContext(ctx, `INSERT INTO discord_resources
		(guild_id,resource_key,discord_id,kind,name,managed_marker)
		VALUES ('runtime-guild','rollback-forum','rollback-forum','forum','rollback','fixture') RETURNING id`).Scan(&resourceID))
	require.NoError(t, f.db.QueryRowContext(ctx, `INSERT INTO discord_forums
		(guild_id,resource_id,forum_type,owner_discord_user_id,workspace_project_id,workspace_id)
		SELECT 'runtime-guild',$1,'workspace','runtime-owner',id,workspace_id
		FROM workspace_projects WHERE workspace_id=$2 LIMIT 1 RETURNING id`, resourceID, f.workspaceID).Scan(&forumID))
	require.NoError(t, f.db.QueryRowContext(ctx, `INSERT INTO discord_conversations
		(guild_id,forum_id,thread_id,owner_discord_user_id,workspace_project_id,session_id,agent_profile_id,engine)
		SELECT 'runtime-guild',$1,'rollback-discord-thread','runtime-owner',workspace_project_id,session_id,agent_profile_id,
			(SELECT engine FROM workspace_sessions WHERE id=codex_turn_intents.session_id)
		FROM codex_turn_intents WHERE id=$2 RETURNING id`, forumID, task.Claimed.ID).Scan(&conversationID))
	_, err := f.db.ExecContext(ctx, `UPDATE codex_thread_controls SET discord_conversation_id=$1 WHERE id=$2`, conversationID, task.Claimed.ControlID)
	require.NoError(t, err)
	_, err = f.db.ExecContext(ctx, `UPDATE codex_turn_intents SET discord_conversation_id=$1 WHERE id=$2`, conversationID, task.Claimed.ID)
	require.NoError(t, err)
}
