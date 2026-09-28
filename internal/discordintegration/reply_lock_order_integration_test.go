//go:build integration

package discordintegration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/stretchr/testify/require"
)

// 锁序约定为 Session → Conversation（入队、元数据补报一致）。Discord 回复若先锁 Conversation 再经入队锁 Session，
// 与已持有 Session 的元数据事务交错即死锁，PostgreSQL 会中止其一；三端接力曾因此偶发失败。
func TestDiscordReplyLocksSessionBeforeConversation(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id, name, enabled)
		VALUES ($1, 'lock-order-test', true)`, testGuildID)
	require.NoError(t, err)
	seed := seedDiscordManagerData(t, db)
	service := NewConversationService(db)
	const threadID = "100000000000000761"
	conversationID, err := service.BeginPost(ctx, IncomingMessage{
		GuildID: testGuildID, ForumID: seed.workspaceForumChannelID,
		ThreadID: threadID, MessageID: "100000000000000762",
		DiscordUserID: "1001", DisplayName: "Alice", Username: "alice",
		Title: "Lock order", Body: "初始任务", ConfigurationConfirmed: true,
	})
	require.NoError(t, err)
	var sessionID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT session_id FROM discord_conversations WHERE id=$1`,
		conversationID).Scan(&sessionID))

	// 按约定先持有 Session，模拟并发的元数据补报。
	metadata, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = metadata.Rollback() }()
	var locked uuid.UUID
	require.NoError(t, metadata.QueryRowContext(ctx, `SELECT id FROM workspace_sessions
		WHERE id=$1 FOR NO KEY UPDATE`, sessionID).Scan(&locked))
	replied := make(chan error, 1)
	go func() {
		replied <- service.Reply(ctx, IncomingMessage{GuildID: testGuildID, ThreadID: threadID,
			MessageID: "100000000000000763", DiscordUserID: "1001", DisplayName: "Alice",
			Username: "alice", Body: "<@900> 继续", MentionsBot: true})
	}()
	select {
	case err := <-replied:
		t.Fatalf("回复必须等待已持有的 Session 锁: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	// 回复若已先占用 Conversation，这里将形成环路并被判定死锁或超时。
	_, err = metadata.ExecContext(ctx, `SET LOCAL lock_timeout = '3s'`)
	require.NoError(t, err)
	require.NoError(t, metadata.QueryRowContext(ctx, `SELECT id FROM discord_conversations
		WHERE id = $1::uuid FOR UPDATE`, conversationID).Scan(&locked),
		"持有 Session 的事务必须能继续锁定 Conversation")
	require.NoError(t, metadata.Commit())
	select {
	case err := <-replied:
		require.NoError(t, err, "释放锁后回复必须完成且不能被中止")
	case <-time.After(10 * time.Second):
		t.Fatal("回复在锁释放后未完成")
	}
	var intent string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT turn_intent_id::text FROM discord_input_messages
		WHERE message_id='100000000000000763'`).Scan(&intent))
	require.NotEmpty(t, intent, "回复必须真实入队")
}
