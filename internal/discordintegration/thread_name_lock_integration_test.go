//go:build integration

package discordintegration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/stretchr/testify/require"
)

func TestThreadNameOutboxLocksControlBeforeOutbox(t *testing.T) {
	for _, operation := range []string{"apply", "fail"} {
		for _, newerTitle := range []bool{false, true} {
			name := operation
			if newerTitle {
				name += "/newer-title"
			}
			t.Run(name, func(t *testing.T) {
				db := discordDatabase(t)
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				require.NoError(t, database.Migrate(ctx, db))
				_, err := db.ExecContext(ctx, "INSERT INTO discord_guilds(guild_id,enabled) VALUES ($1,true)", testGuildID)
				require.NoError(t, err)
				seed := seedDiscordManagerData(t, db)
				conversation, err := NewConversationService(db).BeginPost(ctx, IncomingMessage{
					GuildID: testGuildID, ForumID: seed.workspaceForumChannelID, ThreadID: "100000000000000701",
					MessageID: "100000000000000702", DiscordUserID: "1001", DisplayName: "Alice",
					Username: "alice", Title: "原始标题", Body: "标题并发验证", ConfigurationConfirmed: true,
				})
				require.NoError(t, err)
				var controlID uuid.UUID
				require.NoError(t, db.QueryRowContext(ctx, `UPDATE codex_thread_controls SET
					desired_thread_name='首个标题',desired_thread_name_revision=1,applied_thread_name_revision=0
					WHERE discord_conversation_id=$1 RETURNING id`, conversation).Scan(&controlID))
				// 暂缓无关初始卡片，使用真实 Claim/RecordDelivery 建立本次标题投递状态。
				_, err = db.ExecContext(ctx, "UPDATE integration_outbox SET available_at=now()+interval '1 day'")
				require.NoError(t, err)
				require.NoError(t, EnqueueThreadName(ctx, db, controlID, "100000000000000701", "首个标题", 1))
				store := NewSQLoutbox(db)
				item, err := store.Claim(ctx, time.Minute)
				require.NoError(t, err)
				require.NotNil(t, item)
				require.Equal(t, "thread-name:"+controlID.String(), item.OperationKey)
				if operation == "apply" {
					require.NoError(t, store.RecordDelivery(ctx, item, json.RawMessage(`{}`)))
					expired := *item
					expired.LeaseToken += "-expired"
					require.Error(t, store.Apply(ctx, expired), "失效租约不能确认标题")
					var revision int64
					require.NoError(t, db.QueryRowContext(ctx, "SELECT applied_thread_name_revision FROM codex_thread_controls WHERE id=$1", controlID).Scan(&revision))
					require.Zero(t, revision, "先更新 Control 仍必须随租约校验失败回滚")
				}
				tx, err := db.BeginTx(ctx, nil)
				require.NoError(t, err)
				defer func() { _ = tx.Rollback() }()
				var backendPID int
				var locked uuid.UUID
				require.NoError(t, tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&backendPID))
				require.NoError(t, tx.QueryRowContext(ctx, "SELECT id FROM codex_thread_controls WHERE id=$1 FOR UPDATE", controlID).Scan(&locked))
				done := make(chan error, 1)
				go func() {
					if operation == "apply" {
						done <- store.Apply(ctx, *item)
					} else {
						done <- store.FailDelivery(ctx, *item, errors.New("标题投递失败"))
					}
				}()
				require.Eventually(t, func() bool {
					var blocked bool
					return db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))",
						backendPID).Scan(&blocked) == nil && blocked
				}, 3*time.Second, 10*time.Millisecond, "投递确认必须已等待真实 Control 行锁")
				// 旧实现已经持有 Outbox；NOWAIT 直接返回 55P03，无须依赖死锁超时或调度概率。
				require.NoError(t, tx.QueryRowContext(ctx, "SELECT id FROM integration_outbox WHERE id=$1 FOR UPDATE NOWAIT",
					item.ID).Scan(&locked), "等待 Control 时不能提前持有 Outbox")
				if newerTitle {
					_, err = tx.ExecContext(ctx, "UPDATE codex_thread_controls SET desired_thread_name='更新标题',desired_thread_name_revision=2 WHERE id=$1", controlID)
					require.NoError(t, err)
					require.NoError(t, EnqueueThreadName(ctx, tx, controlID, "100000000000000701", "更新标题", 2))
				}
				require.NoError(t, tx.Commit())
				select {
				case result := <-done:
					require.NoError(t, result)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				var status, lastError string
				var appliedRevision int64
				require.NoError(t, db.QueryRowContext(ctx, "SELECT status FROM integration_outbox WHERE id=$1", item.ID).Scan(&status))
				require.NoError(t, db.QueryRowContext(ctx, "SELECT applied_thread_name_revision,COALESCE(thread_name_last_error,'') FROM codex_thread_controls WHERE id=$1",
					controlID).Scan(&appliedRevision, &lastError))
				switch {
				case newerTitle:
					require.Equal(t, "pending", status)
					require.Zero(t, appliedRevision, "旧确认不能覆盖较新标题")
					require.Empty(t, lastError, "旧投递失败不能污染较新标题")
				case operation == "apply":
					require.Equal(t, "completed", status)
					require.EqualValues(t, 1, appliedRevision)
				default:
					require.Equal(t, "failed", status)
					require.Equal(t, "标题投递失败", lastError)
				}
			})
		}
	}
}
