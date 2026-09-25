//go:build integration

package discordintegration

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

func TestDiscordRuntimeCreationPreferencesAndReplyIsolation(t *testing.T) {
	db := discordDatabase(t)
	ctx := t.Context()
	require.NoError(t, database.Migrate(ctx, db))
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id,name,enabled) VALUES ($1,'runtime-test',true)`, testGuildID)
	require.NoError(t, err)
	seed := seedDiscordManagerData(t, db)
	manager := &Manager{db: db}
	service := NewConversationService(db)
	_, err = db.ExecContext(ctx, `INSERT INTO worker_runtimes(worker_id,engine,enabled,status,ssh_listen_address,
 protocol_version,heartbeat_at,model_catalog) VALUES ($1,'claude-code',true,'running',':3333','0.147.0',now(),
 '{"data":[{"id":"claude-only","isDefault":true}]}');`, seed.workerID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE worker_runtimes SET model_catalog='{"data":[{"id":"codex-only"}]}'
 WHERE worker_id=$1 AND engine='codex'`, seed.workerID)
	require.NoError(t, err)
	makePost := func(thread string, engine runtimeidentity.Engine, model string) IncomingMessage {
		return IncomingMessage{GuildID: testGuildID, ForumID: seed.workspaceForumChannelID, ThreadID: thread,
			MessageID: thread + "01", DiscordUserID: "1001", DisplayName: "Alice", Username: "alice", Title: "同名任务", Body: "执行任务",
			Engine: engine, Model: model, ServiceTier: "standard", ConfigurationConfirmed: true, RememberPreferences: model != ""}
	}
	// 默认值影响新帖子；显式 engine 可以覆盖论坛默认值。
	require.NoError(t, manager.SetWorkspaceForumEngine(ctx, seed.workspaceForumID, runtimeidentity.Claude))
	first := makePost("200001", "", "claude-only")
	claudeID, err := service.BeginPost(ctx, first)
	require.NoError(t, err)
	codexID, err := service.BeginPost(ctx, makePost("200002", runtimeidentity.Codex, "codex-only"))
	require.NoError(t, err)
	for engine, conversationID := range map[runtimeidentity.Engine]uuid.UUID{runtimeidentity.Claude: claudeID, runtimeidentity.Codex: codexID} {
		var conversationEngine, sessionEngine, controlEngine string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT conversation.engine,session.engine,control.engine
   FROM discord_conversations conversation JOIN workspace_sessions session ON session.id=conversation.session_id
   JOIN codex_thread_controls control ON control.session_id=session.id WHERE conversation.id=$1`, conversationID).
			Scan(&conversationEngine, &sessionEngine, &controlEngine))
		require.Equal(t, string(engine), conversationEngine)
		require.Equal(t, conversationEngine, sessionEngine)
		require.Equal(t, sessionEngine, controlEngine)
		preference, found, err := loadUserCodexPreferences(ctx, db, testGuildID, "1001", engine)
		require.NoError(t, err)
		require.True(t, found)
		expected := "codex-only"
		if engine == runtimeidentity.Claude {
			expected = "claude-only"
		}
		require.Equal(t, expected, preference.Model)
		models, err := runtimeModelsForEnvironment(ctx, db, seed.workspaceID, engine)
		require.NoError(t, err)
		require.Len(t, models, 1)
		require.Equal(t, expected, models[0].ID)
	}
	require.NoError(t, manager.SetWorkspaceForumEngine(ctx, seed.workspaceForumID, runtimeidentity.Codex))
	repeated, err := service.BeginPost(ctx, first)
	require.NoError(t, err)
	require.Equal(t, claudeID, repeated)
	conflict := first
	conflict.Engine = runtimeidentity.Codex
	_, err = service.BeginPost(ctx, conflict)
	require.ErrorContains(t, err, "不能切换引擎")
	reply := first
	reply.MessageID = "20000102"
	reply.Body = "继续"
	require.NoError(t, service.Reply(ctx, reply))
	var replyEngine string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT control.engine FROM discord_input_messages message
 JOIN codex_turn_intents intent ON intent.id=message.turn_intent_id
 JOIN codex_thread_controls control ON control.id=intent.control_id WHERE message.message_id=$1`, reply.MessageID).Scan(&replyEngine))
	require.Equal(t, "claude-code", replyEngine)
	inheritedID, err := service.BeginPost(ctx, makePost("200003", "", ""))
	require.NoError(t, err)
	var inheritedEngine, inheritedModel string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT engine,model FROM discord_conversations WHERE id=$1`, inheritedID).
		Scan(&inheritedEngine, &inheritedModel))
	require.Equal(t, "codex", inheritedEngine)
	require.Equal(t, "codex-only", inheritedModel)
	// 表单把引擎固定在创建参数中，论坛默认改变不会暗中更换模型目录。
	connector := &DisgoConnector{manager: manager, conversations: service, guildID: testGuildID}
	modal, err := connector.newCodexModal(ctx, seed.workspaceForumChannelID, "1001", "default", runtimeidentity.Claude)
	require.NoError(t, err)
	require.Contains(t, modal.CustomID, ":claude-code")
	body, err := json.Marshal(modal)
	require.NoError(t, err)
	require.Contains(t, string(body), "claude-only")
	require.NotContains(t, string(body), "codex-only")
	_, err = connector.newCodexModal(ctx, seed.workspaceForumChannelID, "1001", "default", "unknown")
	require.Error(t, err)
	_, err = db.ExecContext(ctx, `UPDATE discord_conversations SET engine='codex' WHERE id=$1`, claudeID)
	require.Error(t, err)
	// 运行时停用后不能选择为新默认值；已有帖子引擎不变。
	_, err = db.ExecContext(ctx, `UPDATE worker_runtimes SET enabled=false WHERE worker_id=$1 AND engine='claude-code'`, seed.workerID)
	require.NoError(t, err)
	require.Error(t, manager.SetWorkspaceForumEngine(ctx, seed.workspaceForumID, runtimeidentity.Claude))
	_, err = runtimeModelsForEnvironment(ctx, db, seed.workspaceID, runtimeidentity.Claude)
	require.Error(t, err)
}
