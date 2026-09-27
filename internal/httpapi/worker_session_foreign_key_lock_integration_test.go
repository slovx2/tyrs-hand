//go:build integration

package httpapi

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestSessionMetadataAllowsConcurrentForeignKeyInserts(t *testing.T) {
	for _, operation := range []string{"generated-title", "name", "settings", "lifecycle", "incoming-message"} {
		for _, child := range []string{"client-update", "discord-conversation"} {
			t.Run(operation+"/"+child, func(t *testing.T) {
				db := workerDatabase(t)
				require.NoError(t, database.Migrate(t.Context(), db))
				server, endpoint := workerTestServer(t, db)
				worker, enrollment, err := server.workers.Create(t.Context(), "session-fk-lock", []string{"discord"}, 2)
				require.NoError(t, err)
				_, credential, err := server.workers.Enroll(t.Context(), enrollment)
				require.NoError(t, err)
				client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)
				repositoryID, _, profileID := seedWorkerGitHubQueue(t, db, 8805)
				workspaceID, forumID := seedWorkerWorkspace(t, db, repositoryID, worker.ID)
				projectID := workspaceProjectIDForForum(t, db, forumID)
				sessionID := enqueueTitleSession(t, db, workspaceID, projectID, profileID, "外键锁回归")
				var controlID uuid.UUID
				require.NoError(t, db.QueryRowContext(t.Context(), `UPDATE codex_thread_controls
					SET external_thread_id='session-fk-thread' WHERE session_id=$1 RETURNING id`, sessionID).Scan(&controlID))
				claim, err := client.ClaimSessionTitle(t.Context())
				require.NoError(t, err)
				require.NotNil(t, claim.Task)
				require.Equal(t, sessionID, claim.Task.SessionID)

				ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
				defer cancel()
				tx, err := db.BeginTx(ctx, nil)
				require.NoError(t, err)
				defer func() { _ = tx.Rollback() }()
				var backendPID int
				var locked uuid.UUID
				require.NoError(t, tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&backendPID))
				require.NoError(t, tx.QueryRowContext(ctx, `SELECT id FROM codex_thread_controls
					WHERE id=$1 FOR UPDATE`, controlID).Scan(&locked))
				done := make(chan error, 1)
				go func() {
					if operation == "incoming-message" {
						incoming, incomingErr := db.BeginTx(ctx, nil)
						if incomingErr != nil {
							done <- incomingErr
							return
						}
						defer func() { _ = incoming.Rollback() }()
						_, _, incomingErr = codexcontrol.NewRepository(db, time.Minute).Enqueue(ctx, incoming,
							codexcontrol.EnqueueRequest{SourceType: codexcontrol.SourceWorkspace, SessionID: sessionID,
								MessageLocalID: "second-input", InputSurface: "client", IdempotencyKey: "second-input",
								Instruction: "并发入队", Behavior: "start_when_idle", ReplyPolicy: "silent"})
						if incomingErr == nil {
							incomingErr = incoming.Commit()
						}
						done <- incomingErr
						return
					}
					if operation == "generated-title" {
						done <- client.CompleteSessionTitle(ctx, claim.Task.ID, workerprotocol.SessionTitleCompleteRequest{
							LeaseToken: claim.Task.LeaseToken, TitleRevision: claim.Task.TitleRevision, Title: "生成标题",
						})
						return
					}
					done <- client.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
						WorkspaceID: workspaceID, Generation: 1, Events: []workerprotocol.ThreadMetadataEvent{{
							Kind: operation, ThreadID: "session-fk-thread", Sequence: 1, Name: "同步标题",
							Model: "test-model", Source: "desktop", LifecycleState: "archived",
						}},
					})
				}()
				require.Eventually(t, func() bool {
					var blocked bool
					return db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
						WHERE $1=ANY(pg_blocking_pids(pid)))`, backendPID).Scan(&blocked) == nil && blocked
				}, 3*time.Second, 10*time.Millisecond, "真实 API 必须先持有 Session 再等待 Control")
				assertSessionWriteLocked(t, ctx, db, sessionID)

				// 模拟回合广播与 Desktop Post：持有 Control 后，真实子表插入仍须通过 Session 外键检查。
				_, err = tx.ExecContext(ctx, "SET LOCAL lock_timeout='750ms'")
				require.NoError(t, err)
				var inserted sql.Result
				if child == "client-update" {
					inserted, err = tx.ExecContext(ctx, `INSERT INTO client_updates(session_id,update_type,entity_type,entity_id,payload)
						VALUES ($1,'turn.updated','turn','session-fk-turn','{}')`, sessionID)
				} else {
					inserted, err = tx.ExecContext(ctx, `INSERT INTO discord_conversations(guild_id,forum_id,thread_id,
						owner_discord_user_id,workspace_project_id,session_id,agent_profile_id)
						SELECT guild_id,id,'session-fk-discord-thread',owner_discord_user_id,workspace_project_id,$2,$3
						FROM discord_forums WHERE id=$1`, forumID, sessionID, profileID)
				}
				require.NoError(t, err, "非键字段更新不得阻塞 Session 外键 KEY SHARE 锁")
				rows, err := inserted.RowsAffected()
				require.NoError(t, err)
				require.EqualValues(t, 1, rows, "必须实际插入一条受外键保护的子记录")
				require.NoError(t, tx.Commit())
				select {
				case result := <-done:
					require.NoError(t, result)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if operation == "generated-title" {
					var title, source string
					require.NoError(t, db.QueryRowContext(ctx, `SELECT title,title_source FROM workspace_sessions
						WHERE id=$1`, sessionID).Scan(&title, &source))
					require.Equal(t, "生成标题", title)
					require.Equal(t, "generated", source)
				}
			})
		}
	}
}

func assertSessionWriteLocked(t *testing.T, ctx context.Context, db *sql.DB, sessionID uuid.UUID) {
	t.Helper()
	probe, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = probe.Rollback() }()
	var locked uuid.UUID
	err = probe.QueryRowContext(ctx, `SELECT id FROM workspace_sessions
		WHERE id=$1 FOR NO KEY UPDATE NOWAIT`, sessionID).Scan(&locked)
	var databaseError *pq.Error
	require.ErrorAs(t, err, &databaseError, "仍须串行化同一 Session 的元数据写入")
	require.Equal(t, pq.ErrorCode("55P03"), databaseError.Code)
}
