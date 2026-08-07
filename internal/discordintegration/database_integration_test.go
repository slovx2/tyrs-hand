//go:build integration

package discordintegration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	disgorest "github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcatalog"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/database"
	ghadapter "github.com/slovx2/tyrs-hand/internal/github"
	"github.com/slovx2/tyrs-hand/internal/secrets"
	"github.com/slovx2/tyrs-hand/internal/security"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap"
)

func TestConversationModeSwitching(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id, name, enabled)
		VALUES ($1, 'mode-test', true)`, testGuildID)
	require.NoError(t, err)
	seed := seedDiscordManagerData(t, db)
	service := NewConversationService(db)
	_, err = service.ConversationMode(ctx, testGuildID, "100000000000000499", "1001")
	require.ErrorContains(t, err, "不是 Codex 会话 Post")
	conversationID, err := service.BeginPost(ctx, IncomingMessage{
		GuildID: testGuildID, ForumID: seed.workspaceForumChannelID,
		ThreadID: "100000000000000401", MessageID: "100000000000000402",
		DiscordUserID: "1001", DisplayName: "Alice", Username: "alice",
		Title: "Plan mode", Body: "先规划", ConfigurationConfirmed: false,
	})
	require.NoError(t, err)
	state, err := service.ConversationMode(ctx, testGuildID, "100000000000000401", "1001")
	require.NoError(t, err)
	require.Equal(t, "default", state.Mode)
	require.False(t, state.Busy)

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":"9901","channel_id":"100000000000000401","content":"updated"}`))
	}))
	t.Cleanup(server.Close)
	remote := NewDisgoRemote("token", server.URL, server.Client())
	t.Cleanup(func() { remote.Close(context.Background()) })
	client := &bot.Client{ApplicationID: snowflake.ID(900), Rest: remote.rest}
	connector := &DisgoConnector{manager: &Manager{db: db}, conversations: service,
		guildID: testGuildID, logger: zap.NewNop()}
	connector.onComponent(newComponentEvent(t, client, "100000000000000403",
		"100000000000000401", modeButtonID(state, "plan"), nil))
	state, err = service.ConversationMode(ctx, testGuildID, "100000000000000401", "1001")
	require.NoError(t, err)
	require.Equal(t, "plan", state.Mode)
	require.EqualValues(t, 1, state.Revision)
	connector.onComponent(newComponentEvent(t, client, "100000000000000407",
		"100000000000000401", triggerModeButtonID(state, "discussion"), nil))
	state, err = service.ConversationMode(ctx, testGuildID, "100000000000000401", "1001")
	require.NoError(t, err)
	require.Equal(t, "discussion", state.TriggerMode)

	update, err := service.SetConversationMode(ctx, testGuildID, "100000000000000401",
		"1001", conversationID, state.SettingsRevision, "plan")
	require.NoError(t, err)
	require.False(t, update.Stale)

	update, err = service.SetConversationMode(ctx, testGuildID, "100000000000000401",
		"1001", conversationID, 0, "default")
	require.NoError(t, err)
	require.True(t, update.Stale)
	latest := update.State
	require.Equal(t, "plan", latest.Mode)
	_, err = service.SetConversationMode(ctx, testGuildID, "100000000000000401",
		"1001", uuid.New(), latest.SettingsRevision, "default")
	require.ErrorContains(t, err, "不属于当前")
	_, err = service.SetConversationMode(ctx, testGuildID, "100000000000000401",
		"1001", conversationID, latest.SettingsRevision, "invalid")
	require.ErrorContains(t, err, "目标模式无效")
	connector.onComponent(newComponentEvent(t, client, "100000000000000404",
		"100000000000000401", "codex-mode:bad", nil))

	_, err = db.ExecContext(ctx, `INSERT INTO discord_forum_access
		(forum_id, discord_user_id, access_level) VALUES ($1, '1002', 'readonly')`,
		seed.workspaceForumID)
	require.NoError(t, err)
	_, err = service.ConversationMode(ctx, testGuildID, "100000000000000401", "1002")
	require.ErrorContains(t, err, "Post 创建者")
	_, err = db.ExecContext(ctx, `INSERT INTO discord_forum_access
		(forum_id, discord_user_id, access_level) VALUES ($1, '1003', 'operator')`,
		seed.workspaceForumID)
	require.NoError(t, err)
	_, err = service.ConversationMode(ctx, testGuildID, "100000000000000401", "1003")
	require.ErrorContains(t, err, "Post 创建者")

	require.NoError(t, service.FinalizeConfiguration(ctx, conversationID, "1001"))
	_, err = service.ConversationMode(ctx, testGuildID, "100000000000000401", "1002")
	require.ErrorContains(t, err, "readonly")
	_, err = service.ConversationMode(ctx, testGuildID, "100000000000000401", "1003")
	require.NoError(t, err)
	busy, err := service.ConversationMode(ctx, testGuildID, "100000000000000401", "1001")
	require.NoError(t, err)
	require.True(t, busy.Busy)
	update, err = service.SetConversationMode(ctx, testGuildID, "100000000000000401",
		"1001", conversationID, busy.SettingsRevision, "default")
	require.NoError(t, err)
	require.Equal(t, "default", update.State.Mode)
	operatorConversationID, err := service.BeginPost(ctx, IncomingMessage{
		GuildID: testGuildID, ForumID: seed.workspaceForumChannelID,
		ThreadID: "100000000000000405", MessageID: "100000000000000406",
		DiscordUserID: "1003", DisplayName: "Operator", Username: "operator",
		Title: "Operator post", Body: "由 operator 创建", ConfigurationConfirmed: false,
	})
	require.NoError(t, err)
	_, err = service.ConversationMode(ctx, testGuildID, "100000000000000405", "1003")
	require.NoError(t, err, "等待阶段应允许实际 Post 创建者配置")
	_, err = service.ConversationMode(ctx, testGuildID, "100000000000000405", "1001")
	require.ErrorContains(t, err, "Post 创建者")
	require.NoError(t, service.FinalizeConfiguration(ctx, operatorConversationID, "1003"))
	_, err = db.ExecContext(ctx, `UPDATE discord_conversations SET lifecycle_state = 'archived'
		WHERE id = $1`, conversationID)
	require.NoError(t, err)
	_, err = service.ConversationMode(ctx, testGuildID, "100000000000000401", "1001")
	require.ErrorIs(t, err, codexcontrol.ErrControlArchived)
}

func TestDiscordOutboxClaimUsesEnqueueOrder(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	keys := []string{
		"projection:conversation-reply:test",
		"projection:conversation-reply:test:page:1",
		"projection:conversation-reply:test:page:2",
	}
	for _, key := range keys {
		require.NoError(t, EnqueueTx(ctx, tx, key, "message.create", "channels/test/messages",
			map[string]string{"channelId": "test"}, key))
	}
	require.NoError(t, tx.Commit())

	outbox := NewSQLoutbox(db)
	for _, expected := range keys {
		item, claimErr := outbox.Claim(ctx, time.Minute)
		require.NoError(t, claimErr)
		require.NotNil(t, item)
		require.Equal(t, expected, item.OperationKey)
	}
}

func TestDiscordOutboxReclaimsOnlyExpiredIdempotentDeliveries(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	operations := []struct {
		key, operation string
	}{
		{"expired-update", "message.update"},
		{"expired-delete", "message.delete"},
		{"expired-tag", "thread.tag.toggle"},
		{"expired-create", "message.create"},
	}
	for _, operation := range operations {
		require.NoError(t, NewSQLoutbox(db).Enqueue(ctx, operation.key, operation.operation,
			"route", map[string]string{"channelId": "20", "messageId": "21"}, ""))
		_, err := db.ExecContext(ctx, `UPDATE integration_outbox SET status='sending',
			attempt_count=1, inflight_revision=request_revision,
			inflight_operation_type=operation_type, inflight_route_key=route_key,
			inflight_payload=payload, inflight_nonce=nonce,
			lease_token=$2, lease_expires_at=now()-interval '1 minute'
			WHERE operation_key=$1`, operation.key, strings.Repeat("a", 64))
		require.NoError(t, err)
	}
	store := NewSQLoutbox(db)
	for _, expected := range operations[:3] {
		item, err := store.Claim(ctx, time.Minute)
		require.NoError(t, err)
		require.NotNil(t, item)
		require.Equal(t, expected.key, item.OperationKey)
	}
	var createStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM integration_outbox
		WHERE operation_key='expired-create'`).Scan(&createStatus))
	require.Equal(t, "ambiguous", createStatus)
}

func TestDiscordOutboxApplyCleansDeletedAndReplacedProjections(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	store := NewSQLoutbox(db)

	require.NoError(t, store.Enqueue(ctx, "projection:orphan", "message.create",
		"channels/20/messages", map[string]string{"channelId": "20", "content": "orphan"},
		"orphan"))
	orphan, err := store.Claim(ctx, time.Minute)
	require.NoError(t, err)
	require.NoError(t, store.RecordDelivery(ctx, orphan, json.RawMessage(`{"messageId":"21"}`)))
	require.NoError(t, store.Enqueue(ctx, "projection:orphan", "message.create",
		"channels/20/messages", map[string]string{"channelId": "20", "content": "newer"},
		"orphan"))
	require.NoError(t, store.Apply(ctx, *orphan))
	var orphanStatus, cleanupType string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM integration_outbox
		WHERE operation_key='projection:orphan'`).Scan(&orphanStatus))
	require.Equal(t, "completed", orphanStatus)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT operation_type FROM integration_outbox
		WHERE operation_key='projection-orphan-delete:orphan:21'`).Scan(&cleanupType))
	require.Equal(t, "message.delete", cleanupType)
	_, err = db.ExecContext(ctx, `DELETE FROM integration_outbox
		WHERE operation_key='projection-orphan-delete:orphan:21'`)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id,name,enabled)
		VALUES ($1,'projection-test',true)`, testGuildID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_projections
		(guild_id,projection_key,resource_id,message_id,desired_version,applied_version,desired_payload)
		VALUES ($1,'replace','20','21',2,1,'{}')`, testGuildID)
	require.NoError(t, err)
	require.NoError(t, store.Enqueue(ctx, "projection:replace", "message.update",
		"channels/20/messages/21", map[string]string{
			"channelId": "20", "messageId": "21", "content": "replacement",
		}, ""))
	replacement, err := store.Claim(ctx, time.Minute)
	require.NoError(t, err)
	require.NoError(t, store.RecordDelivery(ctx, replacement, json.RawMessage(`{"messageId":"22"}`)))
	require.NoError(t, store.Apply(ctx, *replacement))
	var messageID string
	var desiredVersion, appliedVersion int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT message_id,desired_version,applied_version
		FROM discord_projections WHERE projection_key='replace'`).
		Scan(&messageID, &desiredVersion, &appliedVersion))
	require.Equal(t, "22", messageID)
	require.Equal(t, desiredVersion, appliedVersion)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT operation_type FROM integration_outbox
		WHERE operation_key='projection-replaced-delete:replace:21'`).Scan(&cleanupType))
	require.Equal(t, "message.delete", cleanupType)
}

func TestDiscordDiscussionTriggerBatchesPendingMessages(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id, name, enabled)
		VALUES ($1, 'discussion-test', true)`, testGuildID)
	require.NoError(t, err)
	seed := seedDiscordManagerData(t, db)
	service := NewConversationService(db)
	conversationID, err := service.BeginPost(ctx, IncomingMessage{
		GuildID: testGuildID, ForumID: seed.workspaceForumChannelID,
		ThreadID: "100000000000000451", MessageID: "100000000000000452",
		DiscordUserID: "1001", DisplayName: "Alice", Username: "alice",
		Title: "Discussion", Body: "初始任务", ConfigurationConfirmed: true,
	})
	require.NoError(t, err)
	state, err := service.ConversationMode(ctx, testGuildID, "100000000000000451", "1001")
	require.NoError(t, err)
	require.Equal(t, "interactive", state.TriggerMode)
	update, err := service.SetTriggerMode(ctx, testGuildID, "100000000000000451",
		"1001", conversationID, state.SettingsRevision, "discussion")
	require.NoError(t, err)
	require.False(t, update.Stale)
	state = update.State
	require.Equal(t, "discussion", state.TriggerMode)
	var controlSettingsRevision int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT settings_revision
		FROM codex_thread_controls WHERE discord_conversation_id = $1`, conversationID).
		Scan(&controlSettingsRevision))
	require.Equal(t, state.SettingsRevision, controlSettingsRevision)

	require.NoError(t, service.Reply(ctx, IncomingMessage{
		GuildID: testGuildID, ThreadID: "100000000000000451",
		MessageID: "100000000000000453", DiscordUserID: "1001",
		DisplayName: "Alice", Username: "alice", Body: "先讨论方案",
	}))
	var pendingIntent sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `SELECT turn_intent_id::text
		FROM discord_input_messages WHERE message_id = '100000000000000453'`).Scan(&pendingIntent))
	require.False(t, pendingIntent.Valid)
	var ordinaryProjectionCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_projections
		WHERE projection_key=$1`, "conversation:"+conversationID.String()+
		":message:100000000000000453").Scan(&ordinaryProjectionCount))
	require.Zero(t, ordinaryProjectionCount)

	require.NoError(t, service.Reply(ctx, IncomingMessage{
		GuildID: testGuildID, ThreadID: "100000000000000451",
		MessageID: "100000000000000454", DiscordUserID: "1001",
		DisplayName: "Alice", Username: "alice", Body: "<@900> 请按讨论执行", MentionsBot: true,
	}))
	var firstIntent, secondIntent, instruction string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT turn_intent_id::text
		FROM discord_input_messages WHERE message_id = '100000000000000453'`).Scan(&firstIntent))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT turn_intent_id::text
		FROM discord_input_messages WHERE message_id = '100000000000000454'`).Scan(&secondIntent))
	require.Equal(t, firstIntent, secondIntent)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT instruction FROM codex_turn_intents
		WHERE id = $1`, firstIntent).Scan(&instruction))
	require.Less(t, strings.Index(instruction, "先讨论方案"), strings.Index(instruction, "请按讨论执行"))
	var immediateHeader string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT desired_payload->'card'->>'header'
		FROM discord_projections WHERE projection_key=$1`, "conversation:"+conversationID.String()+
		":message:100000000000000454").Scan(&immediateHeader))
	require.Equal(t, "⚙️ Codex · 思考中", immediateHeader)

	require.NoError(t, service.Reply(ctx, IncomingMessage{
		GuildID: testGuildID, ThreadID: "100000000000000451",
		MessageID: "100000000000000455", DiscordUserID: "1001",
		DisplayName: "Alice", Username: "alice", Body: "切换前缓存",
	}))
	update, err = service.SetTriggerMode(ctx, testGuildID, "100000000000000451",
		"1001", conversationID, state.SettingsRevision, "interactive")
	require.NoError(t, err)
	state = update.State
	require.NoError(t, service.Reply(ctx, IncomingMessage{
		GuildID: testGuildID, ThreadID: "100000000000000451",
		MessageID: "100000000000000456", DiscordUserID: "1001",
		DisplayName: "Alice", Username: "alice", Body: "切换后的第一条",
	}))
	var transitionCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(DISTINCT turn_intent_id)
		FROM discord_input_messages WHERE message_id IN ('100000000000000455','100000000000000456')`).
		Scan(&transitionCount))
	require.Equal(t, 1, transitionCount)

	update, err = service.SetTriggerMode(ctx, testGuildID, "100000000000000451",
		"1001", conversationID, state.SettingsRevision, "discussion")
	require.NoError(t, err)
	state = update.State
	attachmentMessages := []IncomingMessage{
		{GuildID: testGuildID, ThreadID: "100000000000000451",
			MessageID: "100000000000000458", DiscordUserID: "1001",
			DisplayName: "Alice", Username: "alice", Body: "第一组附件"},
		{GuildID: testGuildID, ThreadID: "100000000000000451",
			MessageID: "100000000000000459", DiscordUserID: "1001",
			DisplayName: "Alice", Username: "alice", Body: "第二组附件"},
	}
	for messageIndex := range attachmentMessages {
		for attachmentIndex := 0; attachmentIndex < 6; attachmentIndex++ {
			id := fmt.Sprintf("attachment-%d-%d", messageIndex, attachmentIndex)
			attachmentMessages[messageIndex].Attachments = append(
				attachmentMessages[messageIndex].Attachments, IncomingAttachment{
					ID: id, URL: "https://cdn.discordapp.com/attachments/test/" + id + ".txt",
					Filename: id + ".txt", MediaType: "text/plain", Size: 10,
				})
		}
		require.NoError(t, service.Reply(ctx, attachmentMessages[messageIndex]))
	}
	require.NoError(t, service.Reply(ctx, IncomingMessage{
		GuildID: testGuildID, ThreadID: "100000000000000451",
		MessageID: "100000000000000460", DiscordUserID: "1001",
		DisplayName: "Alice", Username: "alice", Body: "<@900> 检查附件", MentionsBot: true,
	}))
	var attachmentInstruction string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT intent.instruction
		FROM codex_turn_intents intent JOIN discord_input_messages message
			ON message.turn_intent_id = intent.id
		WHERE message.message_id = '100000000000000460'`).Scan(&attachmentInstruction))
	require.Contains(t, attachmentInstruction, "共有 12 个附件，仅携带时间最新的 10 个")

	_, err = db.ExecContext(ctx, `INSERT INTO discord_input_messages
		(message_id, conversation_id, discord_user_id, display_name, username,
		 access_snapshot, body, received_at)
		SELECT 'overflow-' || lpad(value::text, 3, '0'), $1, '1001', 'Alice', 'alice',
			'owner', '讨论消息 ' || value::text,
			now() - ((206 - value) * interval '1 second')
		FROM generate_series(1, 205) value`, conversationID)
	require.NoError(t, err)
	require.NoError(t, service.Reply(ctx, IncomingMessage{
		GuildID: testGuildID, ThreadID: "100000000000000451",
		MessageID: "100000000000000457", DiscordUserID: "1001",
		DisplayName: "Alice", Username: "alice", Body: "<@900> 汇总", MentionsBot: true,
	}))
	var batched, skipped int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT
		count(*) FILTER (WHERE turn_intent_id IS NOT NULL),
		count(*) FILTER (WHERE status = 'skipped')
		FROM discord_input_messages WHERE message_id LIKE 'overflow-%'
			OR message_id = '100000000000000457'`).Scan(&batched, &skipped))
	require.Equal(t, 200, batched)
	require.Equal(t, 6, skipped)
}

func TestDiscordDiscussionFollowupConsumesActionablePlanWithoutMention(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id,name,enabled)
		VALUES ($1,'discussion-plan-test',true)`, testGuildID)
	require.NoError(t, err)
	seed := seedDiscordManagerData(t, db)
	service := NewConversationService(db)
	conversationID, err := service.BeginPost(ctx, IncomingMessage{
		GuildID: testGuildID, ForumID: seed.workspaceForumChannelID,
		ThreadID: "100000000000000461", MessageID: "100000000000000462",
		DiscordUserID: "1001", DisplayName: "Alice", Username: "alice",
		Title: "Discussion plan", Body: "先给计划", ConfigurationConfirmed: true,
	})
	require.NoError(t, err)
	state, err := service.ConversationMode(ctx, testGuildID, "100000000000000461", "1001")
	require.NoError(t, err)
	_, err = service.SetTriggerMode(ctx, testGuildID, "100000000000000461", "1001",
		conversationID, state.SettingsRevision, "discussion")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_intents SET status='completed',
		result='{"finalAnswer":"plan","finalOutputType":"plan","turnId":"turn-plan"}'::jsonb,
		finished_at=now() WHERE discord_conversation_id=$1`, conversationID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE codex_thread_controls SET status='idle',
		active_intent_id=NULL WHERE discord_conversation_id=$1`, conversationID)
	require.NoError(t, err)

	require.NoError(t, service.Reply(ctx, IncomingMessage{
		GuildID: testGuildID, ThreadID: "100000000000000461",
		MessageID: "100000000000000463", DiscordUserID: "1001",
		DisplayName: "Alice", Username: "alice", Body: "这个计划需要调整",
	}))
	var intentID sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `SELECT turn_intent_id::text
		FROM discord_input_messages WHERE message_id='100000000000000463'`).Scan(&intentID))
	require.True(t, intentID.Valid, "有效 Plan 后的第一条讨论消息应直接触发下一轮")
}

func TestDiscordMessageEditReservesLatestTurnAndFreezesDiscussion(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id,name,enabled)
		VALUES ($1,'message-edit-test',true)`, testGuildID)
	require.NoError(t, err)
	seed := seedDiscordManagerData(t, db)
	service := NewConversationService(db)
	conversationID, err := service.BeginPost(ctx, IncomingMessage{
		GuildID: testGuildID, ForumID: seed.workspaceForumChannelID,
		ThreadID: "100000000000000471", MessageID: "100000000000000472",
		DiscordUserID: "1001", DisplayName: "Alice", Username: "alice",
		Title: "Editable", Body: "原始请求", ConfigurationConfirmed: true,
	})
	require.NoError(t, err)
	state, err := service.ConversationMode(ctx, testGuildID, "100000000000000471", "1001")
	require.NoError(t, err)
	_, err = service.SetTriggerMode(ctx, testGuildID, "100000000000000471", "1001",
		conversationID, state.SettingsRevision, "discussion")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_intents SET status='completed',
		confirmed_codex_turn_id='turn-original',finished_at=now()
		WHERE discord_message_id='100000000000000472'`)
	require.NoError(t, err)
	desktopIntentID := uuid.New()
	_, err = db.ExecContext(ctx, `INSERT INTO codex_turn_intents
		(id,control_id,sequence_no,operation,behavior,resolved_action,source_type,input_surface,
		 discord_conversation_id,session_id,workspace_project_id,agent_profile_id,idempotency_key,
		 instruction,status,confirmed_codex_turn_id,confirmed_at,finished_at,reply_status,
		 result_delivery_status,projection_anchor)
		SELECT $1,control_id,2,'turn_input','steer_if_active','steer','workspace_session',
		'desktop',discord_conversation_id,session_id,workspace_project_id,agent_profile_id,$2,$3,
		'completed','turn-original',now(),now(),'skipped','delivered','desktop-replay-anchor'
		FROM codex_turn_intents WHERE discord_message_id='100000000000000472'`,
		desktopIntentID, "message-edit-desktop-replay", "Desktop 中间补充")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE codex_thread_controls SET next_sequence_no=3
		WHERE id=(SELECT control_id FROM codex_turn_intents WHERE id=$1)`, desktopIntentID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_forum_access
		(forum_id,discord_user_id,access_level) VALUES ($1,'1003','operator')`,
		seed.workspaceForumID)
	require.NoError(t, err)
	require.NoError(t, service.Reply(ctx, IncomingMessage{
		GuildID: testGuildID, ThreadID: "100000000000000471",
		MessageID: "100000000000000473", DiscordUserID: "1003",
		DisplayName: "Operator", Username: "operator", Body: "补充 steer", MentionsBot: true,
	}))
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_intents SET status='completed',
		confirmed_codex_turn_id='turn-original',resolved_action='steer',finished_at=now()
		WHERE discord_message_id='100000000000000473'`)
	require.NoError(t, err)
	require.NoError(t, service.Reply(ctx, IncomingMessage{
		GuildID: testGuildID, ThreadID: "100000000000000471",
		MessageID: "100000000000000474", DiscordUserID: "1001",
		DisplayName: "Alice", Username: "alice", Body: "待提交多人讨论",
	}))
	outcome, err := service.HandleMessageEdit(ctx, testGuildID, "100000000000000471",
		"100000000000000474", "1001", "待提交多人讨论（已编辑）", time.Now())
	require.NoError(t, err)
	require.Equal(t, MessageEditBuffered, outcome)

	outcome, err = service.HandleMessageEdit(ctx, testGuildID, "100000000000000471",
		"100000000000000473", "1003", "修改后的 steer", time.Now())
	require.NoError(t, err)
	require.Equal(t, MessageEditReserved, outcome)
	var replacementID, targetID, primaryMessageID, instruction, phase string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id::text,target_intent_id::text,
		discord_message_id,instruction,replacement_phase FROM codex_turn_intents
		WHERE operation='replace_last_turn' AND discord_conversation_id=$1`, conversationID).
		Scan(&replacementID, &targetID, &primaryMessageID, &instruction, &phase))
	require.NotEmpty(t, targetID)
	require.Equal(t, "100000000000000474", primaryMessageID)
	require.Equal(t, "reserved", phase)
	require.Less(t, strings.Index(instruction, "原始请求"),
		strings.Index(instruction, "Desktop 中间补充"))
	require.Less(t, strings.Index(instruction, "Desktop 中间补充"),
		strings.Index(instruction, "修改后的 steer"))
	require.Less(t, strings.Index(instruction, "修改后的 steer"),
		strings.Index(instruction, "待提交多人讨论（已编辑）"))
	var boundCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_input_messages
		WHERE turn_intent_id=$1`, replacementID).Scan(&boundCount))
	require.Equal(t, 3, boundCount)

	outcome, err = service.HandleMessageEdit(ctx, testGuildID, "100000000000000471",
		"100000000000000473", "1003", "再次编辑较早消息", time.Now())
	require.NoError(t, err)
	require.Equal(t, MessageEditNotLatest, outcome)
	outcome, err = service.HandleMessageEdit(ctx, testGuildID, "100000000000000471",
		"100000000000000474", "1001", "最新参与者修订", time.Now())
	require.NoError(t, err)
	require.Equal(t, MessageEditCoalesced, outcome)
	var runningTagEnabled bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT (payload->>'enabled')::boolean
		FROM integration_outbox WHERE operation_key=$1`,
		"conversation-running-tag:"+conversationID.String()).Scan(&runningTagEnabled))
	require.True(t, runningTagEnabled)
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_intents
		SET status='canceled',replacement_phase='terminal' WHERE id=$1`, replacementID)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT (payload->>'enabled')::boolean
		FROM integration_outbox WHERE operation_key=$1`,
		"conversation-running-tag:"+conversationID.String()).Scan(&runningTagEnabled))
	require.False(t, runningTagEnabled)
}

func TestDiscordStartupPreferencesCrossForumAndMemberJoin(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id, name, enabled)
		VALUES ($1, 'preferences-test', true)`, testGuildID)
	require.NoError(t, err)
	seed := seedDiscordManagerData(t, db)
	secondForumChannelID := "100000000000000512"
	secondResource := insertDiscordResource(t, db, "forum.workspace.preferences",
		secondForumChannelID, "forum", "preferences", seed.codexCategoryID)
	var secondProjectID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_projects
		(workspace_id, relative_path, name, project_kind, availability_status,
		 branch, head_sha, dirty, remote_url, last_seen_at)
		VALUES ($1,'workspaces/second','second','git','available','main','seed-head',false,
		 'https://example.invalid/second.git',now()) RETURNING id`, seed.workspaceID).
		Scan(&secondProjectID))
	var secondForumID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_forums
		(guild_id, resource_id, forum_type, owner_discord_user_id,
		 workspace_project_id, workspace_id)
		VALUES ($1,$2,'workspace','1001',$3,$4) RETURNING id`, testGuildID,
		secondResource, secondProjectID, seed.workspaceID).Scan(&secondForumID))
	require.NoError(t, func() error {
		_, insertErr := db.ExecContext(ctx, `INSERT INTO discord_forum_access
			(forum_id, discord_user_id, access_level) VALUES
			($1,'1003','operator'),($1,'1002','readonly')`, secondForumID)
		return insertErr
	}())
	service := NewConversationService(db)

	firstID, err := service.BeginPost(ctx, IncomingMessage{
		GuildID: testGuildID, ForumID: seed.workspaceForumChannelID,
		ThreadID: "100000000000000513", MessageID: "100000000000000514",
		DiscordUserID: "1001", DisplayName: "Alice", Username: "alice",
		Title: "remember A", Body: "first",
	})
	require.NoError(t, err)
	state, err := service.ConversationMode(ctx, testGuildID, "100000000000000513", "1001")
	require.NoError(t, err)
	runtimeUpdate, err := service.SetRuntimePreferences(ctx, testGuildID,
		"100000000000000513", "1001", firstID, state.SettingsRevision,
		ConversationConfiguration{Model: "gpt-5.6-sol", ReasoningEffort: "low",
			ServiceTier: "standard"})
	require.NoError(t, err)
	modeUpdate, err := service.SetConversationMode(ctx, testGuildID,
		"100000000000000513", "1001", firstID, runtimeUpdate.State.SettingsRevision, "plan")
	require.NoError(t, err)
	triggerUpdate, err := service.SetTriggerMode(ctx, testGuildID,
		"100000000000000513", "1001", firstID, modeUpdate.State.SettingsRevision, "discussion")
	require.NoError(t, err)
	require.Equal(t, "discussion", triggerUpdate.State.TriggerMode)
	require.NoError(t, service.FinalizeConfiguration(ctx, firstID, "1001"))

	secondID, err := service.BeginPost(ctx, IncomingMessage{
		GuildID: testGuildID, ForumID: secondForumChannelID,
		ThreadID: "100000000000000515", MessageID: "100000000000000516",
		DiscordUserID: "1001", DisplayName: "Alice", Username: "alice",
		Title: "cross forum", Body: "second",
	})
	require.NoError(t, err)
	secondState, err := service.ConversationMode(ctx, testGuildID, "100000000000000515", "1001")
	require.NoError(t, err)
	require.Equal(t, "gpt-5.6-sol", secondState.Model)
	require.Equal(t, "low", secondState.ReasoningEffort)
	require.Equal(t, "standard", secondState.ServiceTier)
	require.Equal(t, "plan", secondState.Mode)
	require.Equal(t, "discussion", secondState.TriggerMode)
	require.NoError(t, service.FinalizeConfiguration(ctx, secondID, "1001"))
	secondState, err = service.ConversationMode(ctx, testGuildID, "100000000000000515", "1001")
	require.NoError(t, err)

	runtimeUpdate, err = service.SetRuntimePreferences(ctx, testGuildID,
		"100000000000000515", "1001", secondID, secondState.SettingsRevision,
		ConversationConfiguration{Model: "gpt-5.6-terra", ReasoningEffort: "xhigh",
			ServiceTier: "fast"})
	require.NoError(t, err)
	modeUpdate, err = service.SetConversationMode(ctx, testGuildID,
		"100000000000000515", "1001", secondID, runtimeUpdate.State.SettingsRevision, "default")
	require.NoError(t, err)
	_, err = service.SetTriggerMode(ctx, testGuildID, "100000000000000515", "1001",
		secondID, modeUpdate.State.SettingsRevision, "interactive")
	require.NoError(t, err)

	thirdID, err := service.BeginPost(ctx, IncomingMessage{
		GuildID: testGuildID, ForumID: seed.workspaceForumChannelID,
		ThreadID: "100000000000000517", MessageID: "100000000000000518",
		DiscordUserID: "1001", DisplayName: "Alice", Username: "alice",
		Title: "still A", Body: "third",
	})
	require.NoError(t, err)
	thirdState, err := service.ConversationMode(ctx, testGuildID, "100000000000000517", "1001")
	require.NoError(t, err)
	require.Equal(t, "gpt-5.6-sol", thirdState.Model)
	require.Equal(t, "low", thirdState.ReasoningEffort)
	require.Equal(t, "standard", thirdState.ServiceTier)
	require.Equal(t, "plan", thirdState.Mode)
	require.Equal(t, "discussion", thirdState.TriggerMode)
	require.NotEqual(t, uuid.Nil, thirdID)

	require.NoError(t, service.Reply(ctx, IncomingMessage{GuildID: testGuildID,
		ThreadID: "100000000000000515", MessageID: "100000000000000519",
		DiscordUserID: "1003", DisplayName: "Charlie", Username: "charlie", Body: "join"}))
	require.NoError(t, service.Reply(ctx, IncomingMessage{GuildID: testGuildID,
		ThreadID: "100000000000000515", MessageID: "100000000000000520",
		DiscordUserID: "1003", DisplayName: "Charlie", Username: "charlie", Body: "again"}))
	var memberOperations int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM integration_outbox
		WHERE operation_key = $1 AND operation_type = 'thread.member.add'`,
		"conversation-member:"+secondID.String()+":1003").Scan(&memberOperations))
	require.Equal(t, 1, memberOperations)
	require.Error(t, service.Reply(ctx, IncomingMessage{GuildID: testGuildID,
		ThreadID: "100000000000000515", MessageID: "100000000000000521",
		DiscordUserID: "1002", DisplayName: "Bob", Username: "bob", Body: "readonly"}))
}

func TestRefreshAllProjectionsDoesNotRequeueRunningTag(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id, name, enabled)
		VALUES ($1, 'running-tag-refresh-test', true)`, testGuildID)
	require.NoError(t, err)
	seed := seedDiscordManagerData(t, db)
	service := NewConversationService(db)
	conversationID, err := service.BeginPost(ctx, IncomingMessage{
		GuildID: testGuildID, ForumID: seed.workspaceForumChannelID,
		ThreadID: "100000000000000521", MessageID: "100000000000000522",
		DiscordUserID: "1001", DisplayName: "Alice", Username: "alice",
		Title: "Running tag refresh", Body: "等待配置", ConfigurationConfirmed: false,
	})
	require.NoError(t, err)
	operationKey := "conversation-running-tag:" + conversationID.String()
	_, err = db.ExecContext(ctx, `DELETE FROM integration_outbox
		WHERE integration='discord' AND operation_key=$1`, operationKey)
	require.NoError(t, err)

	daemon := &Daemon{manager: &Manager{db: db}, logger: zap.NewNop()}
	daemon.refreshAllProjections(ctx, testGuildID,
		&projectionRemote{guild: RemoteGuild{ID: testGuildID}})
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM integration_outbox
		WHERE integration='discord' AND operation_key=$1`, operationKey).Scan(&count))
	require.Zero(t, count)
}

func TestConversationReplyRegeneratingUpdatesFirstPageAndDeletesOverflow(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id, name, enabled)
		VALUES ($1, 'reply-regenerating-test', true)`, testGuildID)
	require.NoError(t, err)
	seed := seedDiscordManagerData(t, db)
	service := NewConversationService(db)
	conversationID, err := service.BeginPost(ctx, IncomingMessage{
		GuildID: testGuildID, ForumID: seed.workspaceForumChannelID,
		ThreadID: "100000000000000531", MessageID: "100000000000000532",
		DiscordUserID: "1001", DisplayName: "Alice", Username: "alice",
		Title: "Reply regeneration", Body: "生成长回复", ConfigurationConfirmed: false,
	})
	require.NoError(t, err)
	require.NoError(t, ProjectConversationReply(ctx, db, "100000000000000531",
		conversationID, "100000000000000532", uuid.Nil,
		strings.Repeat("长回复内容。", 1000), "agentMessage"))
	baseKey := "conversation-reply:" + conversationID.String() + ":message:100000000000000532"
	var pageCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_projections
		WHERE guild_id=$1 AND (projection_key=$2 OR projection_key LIKE $2 || ':page:%')`,
		testGuildID, baseKey).Scan(&pageCount))
	require.Greater(t, pageCount, 1)
	_, err = db.ExecContext(ctx, `UPDATE discord_projections
		SET message_id='message-' || substr(md5(projection_key),1,16), applied_version=desired_version
		WHERE guild_id=$1 AND (projection_key=$2 OR projection_key LIKE $2 || ':page:%')`,
		testGuildID, baseKey)
	require.NoError(t, err)

	require.NoError(t, ProjectConversationReplyRegenerating(ctx, db, "100000000000000531",
		conversationID, "100000000000000532"))
	var payload string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT desired_payload::text
		FROM discord_projections WHERE guild_id=$1 AND projection_key=$2`,
		testGuildID, baseKey).Scan(&payload))
	require.Contains(t, payload, "消息已编辑，正在重新生成。")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_projections
		WHERE guild_id=$1 AND projection_key LIKE $2 || ':page:%'`,
		testGuildID, baseKey).Scan(&pageCount))
	require.Zero(t, pageCount)
	var deleteCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM integration_outbox
		WHERE integration='discord' AND operation_key LIKE 'projection-delete:' || $1 || ':page:%'
			AND operation_type='message.delete'`, baseKey).Scan(&deleteCount))
	require.Greater(t, deleteCount, 0)
}

func TestConversationStatusSplitsTimelineAndKeepsNaturalCards(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id,name,enabled)
		VALUES ($1,'status-move',true)`, testGuildID)
	require.NoError(t, err)
	seed := seedDiscordManagerData(t, db)
	service := NewConversationService(db)
	conversationID, err := service.BeginPost(ctx, IncomingMessage{
		GuildID: testGuildID, ForumID: seed.workspaceForumChannelID,
		ThreadID: "100000000000000601", MessageID: "100000000000000602",
		DiscordUserID: "1001", DisplayName: "Alice", Username: "alice",
		Title: "Status move", Body: "first", ConfigurationConfirmed: true,
	})
	require.NoError(t, err)
	initialKey := "conversation:" + conversationID.String() + ":message:100000000000000602"
	_, err = db.ExecContext(ctx, `UPDATE discord_projections SET message_id='100000000000000603'
		WHERE guild_id=$1 AND projection_key=$2`, testGuildID, initialKey)
	require.NoError(t, err)
	var intentID, controlID, runID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id, control_id FROM codex_turn_intents
		WHERE discord_message_id='100000000000000602'`).Scan(&intentID, &controlID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_turn_runs
		(control_id,primary_intent_id,attempt,lease_owner,lease_epoch,capability_hash,
		 active_slot,status,collaboration_mode)
		VALUES ($1,$2,1,'worker',1,repeat('a',64),1,'running','default') RETURNING id`,
		controlID, intentID).Scan(&runID))
	require.NoError(t, ProjectConversationStatus(ctx, db, testGuildID, "100000000000000601",
		conversationID, "100000000000000602", runID, ConversationRunning,
		"正在核对状态分段"))
	addEvent := func(eventType string, payload any) {
		tx, beginErr := db.BeginTx(ctx, nil)
		require.NoError(t, beginErr)
		var eventID int64
		require.NoError(t, tx.QueryRowContext(ctx, `INSERT INTO agent_events
			(control_id,run_id,event_type,payload) VALUES ($1,$2,$3,$4) RETURNING id`,
			controlID, runID, eventType, mustJSON(payload)).Scan(&eventID))
		require.NoError(t, ResolveConversationStatusBoundaryTx(ctx, tx, runID, eventID,
			eventType, mustJSON(payload)))
		require.NoError(t, tx.Commit())
	}
	addCommentary := func(id, text string) {
		addEvent("item/completed", map[string]any{"item": map[string]any{
			"id": id, "type": "agentMessage", "phase": "commentary", "text": text,
		}})
		require.NoError(t, ProjectConversationStatus(ctx, db, testGuildID,
			"100000000000000601", conversationID, "100000000000000602", runID,
			ConversationRunning, "正在处理请求。"))
	}
	resolveBoundary := func(inputID string) {
		addEvent("item/completed", map[string]any{"item": map[string]any{
			"id": "user-" + inputID, "type": "userMessage", "clientId": inputID,
		}})
	}
	addCommentary("initial-progress", strings.Repeat("初始输入后的动态。", 600))

	move := func(inputID, cardID string, deliverAfterRegistration bool) string {
		require.NoError(t, service.Reply(ctx, IncomingMessage{
			GuildID: testGuildID, ThreadID: "100000000000000601", MessageID: inputID,
			DiscordUserID: "1001", DisplayName: "Alice", Username: "alice", Body: "steer",
		}))
		key := "conversation:" + conversationID.String() + ":message:" + inputID
		tx, beginErr := db.BeginTx(ctx, nil)
		require.NoError(t, beginErr)
		if !deliverAfterRegistration {
			_, updateErr := tx.ExecContext(ctx, `UPDATE discord_projections SET message_id=$3
				WHERE guild_id=$1 AND projection_key=$2`, testGuildID, key, cardID)
			require.NoError(t, updateErr)
		}
		require.NoError(t, RegisterConversationStatusSteerTx(ctx, tx, runID, conversationID,
			testGuildID, inputID))
		require.NoError(t, tx.Commit())
		resolveBoundary(inputID)
		if deliverAfterRegistration {
			var role string
			require.NoError(t, db.QueryRowContext(ctx, `SELECT role FROM discord_turn_status_cards
				WHERE run_id=$1 AND projection_key=$2`, runID, key).Scan(&role))
			require.Equal(t, "pending", role)
			_, updateErr := db.ExecContext(ctx, `UPDATE discord_projections SET message_id=$3
				WHERE guild_id=$1 AND projection_key=$2`, testGuildID, key, cardID)
			require.NoError(t, updateErr)
			tx, beginErr = db.BeginTx(ctx, nil)
			require.NoError(t, beginErr)
			require.NoError(t, promotePendingConversationStatusTx(ctx, tx, testGuildID, key))
			require.NoError(t, tx.Commit())
		}
		return key
	}
	secondKey := move("100000000000000604", "100000000000000605", true)
	addCommentary("second-progress", "第一次引导后的动态")
	thirdKey := move("100000000000000606", "100000000000000607", false)
	addCommentary("third-progress", "第二次引导后的动态")
	registerPending := func(inputID string) string {
		require.NoError(t, service.Reply(ctx, IncomingMessage{
			GuildID: testGuildID, ThreadID: "100000000000000601", MessageID: inputID,
			DiscordUserID: "1001", DisplayName: "Alice", Username: "alice", Body: "steer",
		}))
		key := "conversation:" + conversationID.String() + ":message:" + inputID
		tx, beginErr := db.BeginTx(ctx, nil)
		require.NoError(t, beginErr)
		require.NoError(t, RegisterConversationStatusSteerTx(ctx, tx, runID, conversationID,
			testGuildID, inputID))
		require.NoError(t, tx.Commit())
		resolveBoundary(inputID)
		return key
	}
	fourthKey := registerPending("100000000000000608")
	addCommentary("fourth-progress", "第三次引导后的动态")
	fifthKey := registerPending("100000000000000609")
	addCommentary("fifth-progress", "第四次引导后的动态")
	deliverPending := func(key, cardID string) {
		_, updateErr := db.ExecContext(ctx, `UPDATE discord_projections SET message_id=$3
			WHERE guild_id=$1 AND projection_key=$2`, testGuildID, key, cardID)
		require.NoError(t, updateErr)
		tx, beginErr := db.BeginTx(ctx, nil)
		require.NoError(t, beginErr)
		require.NoError(t, promotePendingConversationStatusTx(ctx, tx, testGuildID, key))
		require.NoError(t, tx.Commit())
	}
	// 较新的卡先送达，较旧卡晚到时只能成为历史卡，不能覆盖 current。
	deliverPending(fifthKey, "100000000000000611")
	deliverPending(fourthKey, "100000000000000610")

	rows, err := db.QueryContext(ctx, `SELECT card.projection_key, card.role,
		projection.desired_payload->'card'->>'header',
		COALESCE(projection.desired_payload->'card'->>'timeline',''),
		COALESCE(projection.desired_payload->'card'->'buttons'->0->>'url','')
		FROM discord_turn_status_cards card JOIN discord_projections projection
		ON projection.guild_id=card.guild_id AND projection.projection_key=card.projection_key
		WHERE card.run_id=$1 ORDER BY card.revision`, runID)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	type cardState struct{ key, role, header, timeline, url string }
	var cards []cardState
	for rows.Next() {
		var item cardState
		require.NoError(t, rows.Scan(&item.key, &item.role, &item.header, &item.timeline, &item.url))
		cards = append(cards, item)
	}
	require.NoError(t, rows.Err())
	require.Len(t, cards, 5)
	for _, item := range cards[:4] {
		require.Equal(t, "history", item.role)
		require.Equal(t, "Codex · 已引导对话", item.header)
		require.Empty(t, item.url)
	}
	require.Contains(t, cards[0].timeline, "初始输入后的动态")
	require.NotContains(t, cards[0].timeline, "第一次引导后的动态")
	require.Contains(t, cards[1].timeline, "第一次引导后的动态")
	require.NotContains(t, cards[1].timeline, "第二次引导后的动态")
	require.Contains(t, cards[2].timeline, "第二次引导后的动态")
	require.Contains(t, cards[3].timeline, "第三次引导后的动态")
	require.Equal(t, fifthKey, cards[4].key)
	require.Equal(t, "current", cards[4].role)
	require.Equal(t, "⚙️ Codex · 思考中", cards[4].header)
	require.Contains(t, cards[4].timeline, "第四次引导后的动态")
	require.Empty(t, cards[4].url)
	require.Equal(t, secondKey, cards[1].key)
	require.Equal(t, thirdKey, cards[2].key)
	require.Equal(t, fourthKey, cards[3].key)
	require.NoError(t, rows.Close())
	initialTimeline, err := conversationTimelineForStatusCard(ctx, db, runID,
		initialKey, "正在处理请求。")
	require.NoError(t, err)
	require.Greater(t, len(initialTimeline.Pages), 1)
	require.Contains(t, strings.Join(initialTimeline.Pages, "\n"), "初始输入后的动态")
	require.NotContains(t, strings.Join(initialTimeline.Pages, "\n"), "第一次引导后的动态")

	require.NoError(t, ProjectConversationStatus(ctx, db, testGuildID,
		"100000000000000601", conversationID, "100000000000000602", runID,
		ConversationCompleted, "本轮处理完成。"))
	var initialHeader, latestHeader string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT desired_payload->'card'->>'header'
		FROM discord_projections WHERE guild_id=$1 AND projection_key=$2`,
		testGuildID, initialKey).Scan(&initialHeader))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT desired_payload->'card'->>'header'
		FROM discord_projections WHERE guild_id=$1 AND projection_key=$2`,
		testGuildID, fifthKey).Scan(&latestHeader))
	require.Equal(t, "Codex · 已引导对话", initialHeader)
	require.Equal(t, "✅ Codex · 已完成", latestHeader)
}

func TestDiscordManagerForumsAndProjections(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	manager := NewManager(db, secrets.NewStore(db, box))

	empty, err := manager.Settings(ctx)
	require.NoError(t, err)
	require.True(t, empty.Community)
	require.Error(t, manager.SaveSettings(ctx, SettingsInput{GuildID: "bad"}))
	require.Error(t, manager.SaveSettings(ctx, SettingsInput{GuildID: testGuildID, Enabled: true}))
	require.NoError(t, manager.SaveSettings(ctx, SettingsInput{
		GuildID: testGuildID, Enabled: true, BotToken: "test-token",
		ApplicationID: "100000000000000002", BotUserID: testBotID,
	}))
	settings, err := manager.Settings(ctx)
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.True(t, settings.TokenConfigured)
	token, err := manager.BotToken(ctx)
	require.NoError(t, err)
	require.Equal(t, "test-token", token)

	seed := seedDiscordManagerData(t, db)
	require.Error(t, manager.SaveSettings(ctx, SettingsInput{GuildID: "100000000000000777", BotToken: "x"}))
	require.NoError(t, manager.SetGatewayStatus(ctx, testGuildID, "connected", nil))
	status, err := manager.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, "connected", status.GatewayStatus)
	_, err = db.ExecContext(ctx, `
		INSERT INTO discord_guilds(guild_id, enabled, updated_at)
		VALUES ('100000000000000777', false, now() - interval '1 hour')`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		INSERT INTO discord_members(guild_id, discord_user_id, username, display_name)
		VALUES ('100000000000000777', '1777', 'other-guild', 'Other Guild')`)
	require.NoError(t, err)
	members, err := manager.Members(ctx)
	require.NoError(t, err)
	require.Len(t, members, 3)
	for _, member := range members {
		require.Equal(t, testGuildID, member.GuildID)
	}
	require.NoError(t, manager.ReplaceMembers(ctx, testGuildID, []RemoteMember{
		{DiscordUserID: "1001", Username: "owner-current", DisplayName: "Owner Current"},
		{DiscordUserID: "1002", Username: "readonly-current", DisplayName: "Readonly Current"},
		{DiscordUserID: "1003", Username: "operator-current", DisplayName: "Operator Current"},
		{DiscordUserID: "1004", Username: "automation", DisplayName: "Automation", IsBot: true},
	}))
	members, err = manager.Members(ctx)
	require.NoError(t, err)
	require.Len(t, members, 3)
	require.Contains(t, members, Member{
		GuildID: testGuildID, DiscordUserID: "1001",
		Username: "owner-current", DisplayName: "Owner Current",
		Bound: true, GitHubLogin: "alice",
	})
	var botActive bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT active FROM discord_members
		WHERE guild_id=$1 AND discord_user_id='1004'`, testGuildID).Scan(&botActive))
	require.True(t, botActive)
	require.Error(t, manager.ReplaceMembers(ctx, testGuildID, []RemoteMember{{
		DiscordUserID: "invalid", Username: "invalid", DisplayName: "Invalid",
	}}))
	var ownerActive bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT active FROM discord_members
		WHERE guild_id=$1 AND discord_user_id='1001'`, testGuildID).Scan(&ownerActive))
	require.True(t, ownerActive, "失败的成员快照不能把既有成员批量标记为 inactive")

	remoteGuild := RemoteGuild{ID: testGuildID, CommunityEnabled: true, Channels: []RemoteChannel{
		{ID: seed.codexCategoryID, Name: "Codex 会话 01", Kind: "category"},
	}}
	_, err = manager.WorkspaceProjectForumPlan(ctx, remoteGuild,
		seed.workspaceProjectID, "")
	require.Error(t, err, "已有活跃 Forum 时不能再创建新配对")
	serverPlan, err := manager.ServerInitializationPlan(ctx, remoteGuild, InitializationIncremental)
	require.NoError(t, err)
	require.True(t, serverPlan.Preflight.Safe)
	require.NotEmpty(t, serverPlan.Actions)
	environments, err := manager.Workspaces(ctx)
	require.NoError(t, err)
	require.Len(t, environments, 1)
	require.Len(t, environments[0].Projects, 1)
	require.Equal(t, seed.workspaceProjectID, environments[0].Projects[0].ID)
	require.Len(t, environments[0].Projects[0].Forums, 1)
	require.NotNil(t, environments[0].WorkerID)
	require.Equal(t, seed.workerID, *environments[0].WorkerID)
	require.Error(t, manager.SetForumAccess(ctx, seed.workspaceForumID, "1002", "admin", seed.administratorID))
	require.NoError(t, manager.SetForumAccess(ctx, seed.workspaceForumID, "1002", AccessReadOnly, seed.administratorID))
	require.NoError(t, manager.SetForumAccess(ctx, seed.workspaceForumID, "1003", AccessOperator, seed.administratorID))
	require.NoError(t, manager.DeleteForumAccess(ctx, seed.workspaceForumID, "1002"))
	var permissionPayload []byte
	require.NoError(t, db.QueryRowContext(ctx, `SELECT payload FROM integration_outbox
		WHERE operation_key = $1`, "forum-permissions:"+seed.workspaceForumID.String()).Scan(&permissionPayload))
	require.Contains(t, string(permissionPayload), "1003")

	daemon := &Daemon{manager: manager, logger: zap.NewNop()}
	require.NoError(t, daemon.refreshSystemStatus(ctx, testGuildID))
	require.NoError(t, daemon.refreshSystemAlerts(ctx, testGuildID))
	projectionRemote := &projectionRemote{guild: RemoteGuild{ID: testGuildID, Channels: []RemoteChannel{{
		ID: seed.repositoryForumChannelID, Kind: "forum", Tags: map[string]string{"Needs Attention": "7001"},
	}}}}
	require.NoError(t, daemon.refreshTaskProjections(ctx, testGuildID, projectionRemote))
	completeOutboxForTest(t, ctx, db, "task-post:"+seed.workItemID.String(),
		json.RawMessage(`{"threadId":"7101","messageId":"7102"}`))
	task := taskProjection{
		WorkItemID: seed.workItemID.String(), ForumDBID: seed.repositoryForumID.String(),
		ForumDiscordID: seed.repositoryForumChannelID, Kind: "issue", Number: 7, Title: "Needs help",
		WorkItemState: "open", JobStatus: "running", ThreadID: "7101", StarterMessageID: "7102",
		LastState: "Needs Attention",
	}
	require.NoError(t, daemon.projectTask(ctx, task, map[string]string{"Running": "7001", "Completed": "7002"}))
	task.WorkItemState, task.JobStatus, task.LastState = "closed", "", "Running"
	task.ClosedAt = sql.NullTime{Time: time.Now().Add(-8 * 24 * time.Hour), Valid: true}
	require.NoError(t, daemon.projectTask(ctx, task, map[string]string{"Completed": "7002"}))
	completeOutboxForTest(t, ctx, db, "task-card:"+seed.workItemID.String(),
		json.RawMessage(`{"messageId":"7103"}`))
	completeOutboxForTest(t, ctx, db, "task-archive:"+seed.workItemID.String(), nil)
	var taskType string
	var taskPayload, statusPayload, alertsPayload []byte
	require.NoError(t, db.QueryRowContext(ctx, `SELECT operation_type FROM integration_outbox
		WHERE operation_key = $1`, "task-post:"+seed.workItemID.String()).Scan(&taskType))
	require.Equal(t, "forum.post.create", taskType)
	var taskMessageID, replacementDeleteType string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT starter_message_id
		FROM discord_task_posts WHERE work_item_id=$1`, seed.workItemID).Scan(&taskMessageID))
	require.Equal(t, "7103", taskMessageID)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT operation_type FROM integration_outbox
		WHERE operation_key=$1`, "message-replaced-delete:task-card:"+
		seed.workItemID.String()+":7102").Scan(&replacementDeleteType))
	require.Equal(t, "message.delete", replacementDeleteType)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT payload FROM integration_outbox
		WHERE operation_key = $1`, "task-post:"+seed.workItemID.String()).Scan(&taskPayload))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT payload FROM integration_outbox
		WHERE operation_key = 'projection:system.status'`).Scan(&statusPayload))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT payload FROM integration_outbox
		WHERE operation_key = 'projection:system.alerts'`).Scan(&alertsPayload))
	for _, payload := range [][]byte{taskPayload, statusPayload, alertsPayload} {
		require.Contains(t, string(payload), `"card"`)
		require.NotContains(t, string(payload), `"embeds"`)
	}
	intentID := insertProjectionIntent(t, db, seed.workItemID, seed.repositoryID,
		"projection-job-retry", "alice")
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_intents SET status = 'completed',
		finished_at = now(), created_at = now() + interval '1 second' WHERE id = $1`, intentID)
	require.NoError(t, err)
	require.NoError(t, daemon.refreshSystemStatus(ctx, testGuildID))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT payload FROM integration_outbox
		WHERE operation_key = 'projection:system.status'`).Scan(&statusPayload))
	require.Contains(t, string(statusPayload), "失败 `0`")
	require.Equal(t, "Completed", projectedTaskState("closed", ""))
	require.Equal(t, "Running", projectedTaskState("open", "queued"))
	require.Equal(t, "Failed", projectedTaskState("open", "failed"))
	require.Equal(t, "Completed", projectedTaskState("open", "completed"))
	require.Equal(t, []string{"7001"}, taskTagIDs(map[string]string{"Running": "7001"}, "Running"))
	require.Len(t, []rune(taskThreadName(taskProjection{Number: 1, Title: string(make([]rune, 120))})), 100)
	testGatewayHandlers(t, ctx, db, manager, seed)
	testDiscordRecoveryOrchestration(t, ctx, db, manager, seed)
}

func TestWorkspaceProjectForumLifecycle(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	manager := NewManager(db, secrets.NewStore(db, box))
	require.NoError(t, manager.SaveSettings(ctx, SettingsInput{
		GuildID: testGuildID, Enabled: true, BotToken: "test-token",
		ApplicationID: "100000000000000002", BotUserID: testBotID,
	}))
	seed := seedDiscordManagerData(t, db)
	remoteGuild := RemoteGuild{ID: testGuildID, CommunityEnabled: true, Channels: []RemoteChannel{
		{ID: seed.codexCategoryID, Name: "Codex 会话 01", Kind: "category"},
	}}

	require.NoError(t, manager.DisableWorkspaceForum(ctx, seed.workspaceForumID))
	plan, err := manager.WorkspaceProjectForumPlan(ctx, remoteGuild,
		seed.workspaceProjectID, "")
	require.NoError(t, err)
	require.Equal(t, "alice-repo", plan.Preflight.Creates[0])
	action := plan.Actions[len(plan.Actions)-1]
	require.Equal(t, "forum.workspace_project.record", action.Kind)
	newForumID := uuid.MustParse(action.ForumID)
	insertDiscordResource(t, db, action.Spec.Key, "100000000000000060",
		"forum", action.Spec.Name, seed.codexCategoryID)
	_, err = manager.executeInitializationAction(ctx, testGuildID, action, nil)
	require.NoError(t, err)

	require.Error(t, manager.RestoreWorkspaceForum(ctx,
		seed.workspaceProjectID, seed.workspaceForumID),
		"同一项目只能有一个活跃 Forum")
	require.NoError(t, manager.DisableWorkspaceForum(ctx, newForumID))
	require.NoError(t, manager.RestoreWorkspaceForum(ctx,
		seed.workspaceProjectID, seed.workspaceForumID))

	_, err = db.ExecContext(ctx, `UPDATE workspace_projects
		SET availability_status='missing' WHERE id=$1`, seed.workspaceProjectID)
	require.NoError(t, err)
	require.NoError(t, manager.DisableWorkspaceForum(ctx, seed.workspaceForumID))
	require.Error(t, manager.RestoreWorkspaceForum(ctx,
		seed.workspaceProjectID, seed.workspaceForumID))
	_, err = manager.WorkspaceProjectForumPlan(ctx, remoteGuild,
		seed.workspaceProjectID, "")
	require.Error(t, err)

	var environmentCount int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT count(*) FROM worker_workspaces WHERE id=$1`,
		seed.workspaceID).Scan(&environmentCount))
	require.Equal(t, 1, environmentCount, "停用最后一个 Forum 不得删除长期环境")
}

func TestConversationLifecycleProjectionAndRestore(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id, name, enabled)
		VALUES ($1, 'Lifecycle Test', true)`, testGuildID)
	require.NoError(t, err)
	seed := seedDiscordManagerData(t, db)
	var profileID, workspaceID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM agent_profiles
		ORDER BY created_at LIMIT 1`).Scan(&profileID))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT workspace_id
		FROM discord_forums WHERE id = $1`, seed.workspaceForumID).Scan(&workspaceID))
	conversationID, controlID := uuid.New(), uuid.New()
	_, err = db.ExecContext(ctx, `INSERT INTO discord_conversations
		(id, guild_id, forum_id, thread_id, starter_message_id, owner_discord_user_id,
			workspace_project_id, agent_profile_id, title, lifecycle_state, lifecycle_revision)
		VALUES ($1,$2,$3,'100000000000000070','100000000000000071','1001',$4,$5,
		'Lifecycle','archived',3)`, conversationID, testGuildID,
		seed.workspaceForumID, seed.workspaceProjectID, profileID)
	require.NoError(t, err)
	sessionID := bindDiscordConversationSessionForTest(t, db, conversationID)
	_, err = db.ExecContext(ctx, `INSERT INTO codex_thread_controls
		(id, source_type, discord_conversation_id, session_id, workspace_project_id, agent_profile_id,
			external_thread_id, worker_id,
			workspace_id, lifecycle_state, lifecycle_revision)
		VALUES ($1,'workspace_session',$2,$3,$4,$5,'thread-lifecycle',$6,$7,'archived',3)`,
		controlID, conversationID, sessionID, seed.workspaceProjectID, profileID, seed.workerID,
		workspaceID)
	require.NoError(t, err)

	service := NewConversationService(db)
	err = service.Reply(ctx, IncomingMessage{
		GuildID: testGuildID, ThreadID: "100000000000000070",
		MessageID: "100000000000000073", DiscordUserID: "1001",
		DisplayName: "alice", Username: "alice", Body: "should not run",
	})
	require.ErrorIs(t, err, codexcontrol.ErrControlArchived)
	var rejectedMessages int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_input_messages
		WHERE message_id = '100000000000000073'`).Scan(&rejectedMessages))
	require.Zero(t, rejectedMessages)

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, EnqueueConversationLifecycleTx(ctx, tx, conversationID))
	require.NoError(t, tx.Commit())
	var lifecycleCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM integration_outbox
		WHERE operation_key = $1`, "conversation-lifecycle:"+conversationID.String()).
		Scan(&lifecycleCount))
	require.Zero(t, lifecycleCount, "归档卡片完成前不能锁定 Post")
	completeOutboxForTest(t, ctx, db, "conversation-lifecycle-card:"+conversationID.String(),
		json.RawMessage(`{"messageId":"100000000000000072"}`))
	var operationType string
	var payload json.RawMessage
	require.NoError(t, db.QueryRowContext(ctx, `SELECT operation_type, payload
		FROM integration_outbox WHERE operation_key = $1`,
		"conversation-lifecycle:"+conversationID.String()).Scan(&operationType, &payload))
	require.Equal(t, "thread.lifecycle", operationType)
	require.JSONEq(t, `{"channelId":"100000000000000070","conversationId":"`+
		conversationID.String()+`","lifecycleState":"archived","revision":3,`+
		`"archived":true,"locked":true}`, string(payload))
	completeOutboxForTest(t, ctx, db, "conversation-lifecycle:"+conversationID.String(), nil)
	var appliedRevision int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT discord_lifecycle_applied_revision
		FROM discord_conversations WHERE id = $1`, conversationID).Scan(&appliedRevision))
	require.EqualValues(t, 3, appliedRevision)

	staleRevision := int64(2)
	_, err = service.Restore(ctx, testGuildID, "100000000000000070", "1001",
		&staleRevision)
	require.ErrorIs(t, err, ErrLifecycleRevisionStale)
	_, err = service.Restore(ctx, testGuildID, "100000000000000070", "1003", nil)
	require.ErrorIs(t, err, ErrReadOnly)
	revision := int64(3)
	state, err := service.Restore(ctx, testGuildID, "100000000000000070", "1001",
		&revision)
	require.NoError(t, err)
	require.Equal(t, "applying", state.Status)
	require.EqualValues(t, 4, state.Revision)
	var conversationState, controlState, source, requestedBy string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT conversation.lifecycle_state,
		control.lifecycle_state, request.source, request.requested_by_discord_user_id
		FROM discord_conversations conversation
		JOIN codex_thread_controls control ON control.discord_conversation_id = conversation.id
		JOIN codex_thread_lifecycle_requests request ON request.control_id = control.id
		WHERE conversation.id = $1 AND request.id = $2`, conversationID, state.ID).
		Scan(&conversationState, &controlState, &source, &requestedBy))
	require.Equal(t, "unarchive_pending", conversationState)
	require.Equal(t, "unarchive_pending", controlState)
	require.Equal(t, "discord", source)
	require.Equal(t, "1001", requestedBy)
	var staleItem OutboxItem
	var staleID uuid.UUID
	staleItem.LeaseToken = strings.Repeat("b", 64)
	require.NoError(t, db.QueryRowContext(ctx, `UPDATE integration_outbox SET
		status='sending', inflight_revision=request_revision,
		inflight_operation_type=operation_type, inflight_route_key=route_key,
		inflight_payload=payload, inflight_nonce=nonce,
		lease_token=$2, lease_expires_at=now()+interval '1 minute'
		WHERE operation_key=$1 RETURNING id, operation_key, operation_type, route_key,
			payload, COALESCE(nonce,''), attempt_count, max_attempts, request_revision`,
		"conversation-lifecycle:"+conversationID.String(), staleItem.LeaseToken).
		Scan(&staleID, &staleItem.OperationKey, &staleItem.OperationType,
			&staleItem.RouteKey, &staleItem.Payload, &staleItem.Nonce,
			&staleItem.Attempt, &staleItem.MaxAttempts, &staleItem.RequestRevision))
	staleItem.ID = staleID.String()
	require.NoError(t, NewSQLoutbox(db).FailDelivery(ctx, staleItem,
		errors.New("旧 lifecycle 回调失败")))
	var projectionError sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lifecycle_state,
		lifecycle_projection_error FROM discord_conversations WHERE id=$1`,
		conversationID).Scan(&conversationState, &projectionError))
	require.Equal(t, "unarchive_pending", conversationState)
	require.False(t, projectionError.Valid,
		"旧 revision Outbox 失败回调不得污染当前恢复状态")

	_, err = db.ExecContext(ctx, `UPDATE codex_thread_controls SET
		lifecycle_state='active', lifecycle_revision=4 WHERE id=$1`, controlID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE discord_conversations SET
		lifecycle_state='active', lifecycle_revision=4,
		discord_lifecycle_applied_revision=4 WHERE id=$1`, conversationID)
	require.NoError(t, err)
	require.NoError(t, ReconcileConversationLifecycles(ctx, db, testGuildID))
	completeOutboxForTest(t, ctx, db, "conversation-lifecycle:"+conversationID.String(), nil)
	deleteKey := "conversation-lifecycle-delete:" + conversationID.String() + ":4"
	var deleteType string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT operation_type FROM integration_outbox
		WHERE operation_key=$1`, deleteKey).Scan(&deleteType))
	require.Equal(t, "message.delete", deleteType,
		"恢复成功必须先解锁，再删除原归档卡片")
	completeOutboxForTest(t, ctx, db, deleteKey, json.RawMessage(`{}`))
	var lifecycleCardID sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lifecycle_card_message_id
		FROM discord_conversations WHERE id=$1`, conversationID).Scan(&lifecycleCardID))
	require.False(t, lifecycleCardID.Valid)

	intentID, runID := uuid.New(), uuid.New()
	_, err = db.ExecContext(ctx, `INSERT INTO codex_turn_intents
		(id, control_id, sequence_no, source_type, discord_conversation_id, session_id,
			workspace_project_id, agent_profile_id, idempotency_key, status)
		VALUES ($1,$2,1,'workspace_session',$3,$4,$5,$6,$7,'running')`, intentID,
		controlID, conversationID, sessionID, seed.workspaceProjectID, profileID,
		"lifecycle-active-"+intentID.String())
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO codex_turn_runs
		(id, control_id, primary_intent_id, attempt, lease_owner, lease_epoch,
			capability_hash, active_slot, status)
		VALUES ($1,$2,$3,1,'worker',1,$4,1,'running')`, runID, controlID,
		intentID, strings.Repeat("c", 64))
	require.NoError(t, err)
	archive, err := service.Archive(ctx, testGuildID, "100000000000000070", "1001")
	require.NoError(t, err)
	require.Equal(t, "waiting_for_turn", archive.Status)
	repeatedArchive, err := service.Archive(ctx, testGuildID,
		"100000000000000070", "1001")
	require.NoError(t, err)
	require.Equal(t, archive.ID, repeatedArchive.ID)
	require.Equal(t, archive.Revision, repeatedArchive.Revision)
	_, err = service.Archive(ctx, testGuildID, "100000000000000070", "1003")
	require.ErrorIs(t, err, ErrReadOnly)
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_runs SET status='completed',
		active_slot=NULL, finished_at=now() WHERE id=$1`, runID)
	require.NoError(t, err)
	winningRestore, err := service.Restore(ctx, testGuildID,
		"100000000000000070", "1001", nil)
	require.NoError(t, err)
	require.Equal(t, "completed", winningRestore.Status)
	require.Greater(t, winningRestore.Revision, archive.Revision)
	var canceledArchive string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status
		FROM codex_thread_lifecycle_requests WHERE id=$1`, archive.ID).Scan(&canceledArchive))
	require.Equal(t, "canceled", canceledArchive,
		"archive/restore 竞争必须由最新 revision 胜出")

	gatewayConversationID, gatewayControlID := uuid.New(), uuid.New()
	const gatewayThreadID = "100000000000000080"
	_, err = db.ExecContext(ctx, `INSERT INTO discord_conversations
		(id, guild_id, forum_id, thread_id, starter_message_id, owner_discord_user_id,
			workspace_project_id, agent_profile_id, title, lifecycle_state, lifecycle_revision)
		VALUES ($1,$2,$3,$4,'100000000000000081','1001',$5,$6,
			'Gateway Lifecycle','archived',7)`, gatewayConversationID, testGuildID,
		seed.workspaceForumID, gatewayThreadID, seed.workspaceProjectID, profileID)
	require.NoError(t, err)
	gatewaySessionID := bindDiscordConversationSessionForTest(t, db, gatewayConversationID)
	_, err = db.ExecContext(ctx, `INSERT INTO codex_thread_controls
		(id, source_type, discord_conversation_id, session_id, workspace_project_id, agent_profile_id,
			external_thread_id, worker_id,
			workspace_id, lifecycle_state, lifecycle_revision)
		VALUES ($1,'workspace_session',$2,$3,$4,$5,'thread-lifecycle-gateway',$6,$7,
		'archived',7)`, gatewayControlID, gatewayConversationID, gatewaySessionID,
		seed.workspaceProjectID, profileID, seed.workerID, workspaceID)
	require.NoError(t, err)

	require.NoError(t, ReconcileConversationLifecycles(ctx, db, testGuildID))
	var cardStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM integration_outbox
		WHERE operation_key = $1`,
		"conversation-lifecycle-card:"+gatewayConversationID.String()).Scan(&cardStatus))
	require.Equal(t, "pending", cardStatus)

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodPatch &&
			strings.Contains(request.URL.Path, "/messages/@original") {
			_, _ = response.Write([]byte(`{"id":"9902","channel_id":"` +
				gatewayThreadID + `","content":"updated"}`))
			return
		}
		http.NotFound(response, request)
	}))
	t.Cleanup(server.Close)
	remote := NewDisgoRemote("token", server.URL, server.Client())
	t.Cleanup(func() { remote.Close(context.Background()) })
	client := &bot.Client{ApplicationID: snowflake.ID(900), Rest: remote.rest}
	connector := &DisgoConnector{
		manager: &Manager{db: db}, conversations: service,
		guildID: testGuildID, logger: zap.NewNop(),
	}
	connector.onComponent(newComponentEvent(t, client, "100000000000000082",
		gatewayThreadID, "codex-restore:"+gatewayConversationID.String()+":7", nil))
	var gatewayState string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lifecycle_state
		FROM discord_conversations WHERE id = $1`, gatewayConversationID).Scan(&gatewayState))
	require.Equal(t, "unarchive_pending", gatewayState)
	var restoreRequests int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*)
		FROM codex_thread_lifecycle_requests
		WHERE control_id = $1 AND source = 'discord' AND desired_state = 'active'`,
		gatewayControlID).Scan(&restoreRequests))
	require.Equal(t, 1, restoreRequests)

	_, err = db.ExecContext(ctx, `UPDATE discord_conversations SET
		lifecycle_state = 'archived', lifecycle_revision = 9,
		discord_lifecycle_applied_revision = 9 WHERE id = $1`, gatewayConversationID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE codex_thread_controls SET
		lifecycle_state = 'archived', lifecycle_revision = 9 WHERE id = $1`,
		gatewayControlID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE integration_outbox SET status = 'completed'
		WHERE operation_key = $1`,
		"conversation-lifecycle-card:"+gatewayConversationID.String())
	require.NoError(t, err)
	var thread discord.GuildThread
	require.NoError(t, json.Unmarshal([]byte(`{
		"id":"`+gatewayThreadID+`",
		"guild_id":"`+testGuildID+`",
		"parent_id":"100000000000000012",
		"type":11,
		"name":"Gateway Lifecycle",
		"owner_id":"1001",
		"message_count":1,
		"member_count":1,
		"rate_limit_per_user":0,
		"thread_metadata":{
			"archived":false,
			"auto_archive_duration":10080,
			"archive_timestamp":"2026-07-18T00:00:00Z",
			"locked":false
		}
	}`), &thread))
	connector.onThreadUpdate(&events.ThreadUpdate{GenericThread: &events.GenericThread{
		GenericEvent: events.NewGenericEvent(client, 1, 0),
		Thread:       thread,
		ThreadID:     snowflake.MustParse(gatewayThreadID),
		GuildID:      snowflake.MustParse(testGuildID),
	}})
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM integration_outbox
		WHERE operation_key = $1`,
		"conversation-lifecycle-card:"+gatewayConversationID.String()).Scan(&cardStatus))
	require.Equal(t, "pending", cardStatus,
		"Discord 状态漂移后应重新投影 app-server 的权威生命周期")
	var restoreRequestsAfterDrift int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*)
		FROM codex_thread_lifecycle_requests WHERE control_id = $1`,
		gatewayControlID).Scan(&restoreRequestsAfterDrift))
	require.Equal(t, restoreRequests, restoreRequestsAfterDrift,
		"Discord THREAD_UPDATE 不得反向创建 Codex 生命周期请求")

	_, err = db.ExecContext(ctx, `UPDATE discord_conversations SET
		lifecycle_state='active', lifecycle_revision=10,
		discord_lifecycle_applied_revision=10 WHERE id=$1`, gatewayConversationID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE codex_thread_controls SET
		lifecycle_state='active', lifecycle_revision=10 WHERE id=$1`, gatewayControlID)
	require.NoError(t, err)
	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, EnqueueConversationLifecycleTx(ctx, tx, gatewayConversationID))
	require.NoError(t, tx.Commit())
	var lifecycleRequestsBeforeHiddenRestore int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*)
		FROM codex_thread_lifecycle_requests WHERE control_id=$1`, gatewayControlID).
		Scan(&lifecycleRequestsBeforeHiddenRestore))
	hiddenRestore, err := service.Restore(ctx, testGuildID, gatewayThreadID, "1001", nil)
	require.NoError(t, err)
	require.Equal(t, "completed", hiddenRestore.Status)
	var lifecycleRequestsAfterHiddenRestore int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*)
		FROM codex_thread_lifecycle_requests WHERE control_id=$1`, gatewayControlID).
		Scan(&lifecycleRequestsAfterHiddenRestore))
	require.Equal(t, lifecycleRequestsBeforeHiddenRestore, lifecycleRequestsAfterHiddenRestore,
		"仅隐藏的 active Post 恢复时不得调用 app-server")
	_, err = db.ExecContext(ctx, `UPDATE integration_outbox SET status='completed'
		WHERE operation_key=$1`, "conversation-lifecycle:"+gatewayConversationID.String())
	require.NoError(t, err)
	thread.ThreadMetadata.Archived = true
	thread.ThreadMetadata.Locked = false
	connector.onThreadUpdate(&events.ThreadUpdate{GenericThread: &events.GenericThread{
		GenericEvent: events.NewGenericEvent(client, 2, 0), Thread: thread,
		ThreadID: snowflake.MustParse(gatewayThreadID), GuildID: snowflake.MustParse(testGuildID),
	}})
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM integration_outbox
		WHERE operation_key=$1`, "conversation-lifecycle:"+gatewayConversationID.String()).
		Scan(&cardStatus))
	require.Equal(t, "completed", cardStatus,
		"Discord 未锁定归档只表示隐藏，不应重新打开 Post 或修改 Codex lifecycle")
}

func TestReconcileConversationProgressCardsUpdatesExistingMessage(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id, name, enabled)
		VALUES ($1, 'Progress Test', true)`, testGuildID)
	require.NoError(t, err)
	conversationID := uuid.New()
	projectionKey := "conversation:" + conversationID.String() + ":message:desktop-input"
	_, err = db.ExecContext(ctx, `INSERT INTO discord_projections
		(guild_id, projection_key, resource_id, message_id, desired_payload)
		VALUES ($1,$2,'100000000000000090','100000000000000091',$3)`, testGuildID,
		projectionKey, mustJSON(map[string]any{
			"card": ComponentCardPayload{AccentColor: cardColorBlurple,
				Header: "## ⚙️ Codex · 处理中", Body: "`1s` · `1190 条更新`"},
			"progress": conversationProgressPayload{State: ConversationRunning,
				Summary: "正在处理请求。", Page: 0},
		}))
	require.NoError(t, err)
	require.NoError(t, ReconcileConversationProgressCards(ctx, db, testGuildID))
	var desiredPayload json.RawMessage
	var operationType string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT projection.desired_payload,
		outbox.operation_type FROM discord_projections projection
		JOIN integration_outbox outbox ON outbox.operation_key='projection:' || projection.projection_key
		WHERE projection.guild_id=$1 AND projection.projection_key=$2`, testGuildID,
		projectionKey).Scan(&desiredPayload, &operationType))
	require.Equal(t, "message.update", operationType)
	require.Contains(t, string(desiredPayload), `"formatVersion": 5`)
	require.Contains(t, string(desiredPayload), "项动态")
	require.NotContains(t, string(desiredPayload), "条更新")
}

func TestReconcileConversationProgressCardsUpdatesOrphanedRun(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id, name, enabled)
		VALUES ($1, 'Orphan Progress Test', true)`, testGuildID)
	require.NoError(t, err)

	projectionKey := "conversation:orphan:message:test"
	missingRunID := uuid.New()
	_, err = db.ExecContext(ctx, `INSERT INTO discord_projections
		(guild_id, projection_key, resource_id, message_id, desired_payload,
		 desired_version, applied_version)
		VALUES ($1,$2,'thread-id','message-id',$3,1,1)`, testGuildID, projectionKey,
		mustJSON(map[string]any{
			"card": ComponentCardPayload{Header: "旧卡片"},
			"progress": conversationProgressPayload{RunID: missingRunID.String(),
				State: ConversationCompleted, Summary: "历史任务已完成。"},
		}))
	require.NoError(t, err)

	require.NoError(t, ReconcileConversationProgressCards(ctx, db, testGuildID))
	var desiredPayload string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT desired_payload::text
		FROM discord_projections WHERE guild_id=$1 AND projection_key=$2`,
		testGuildID, projectionKey).Scan(&desiredPayload))
	require.Contains(t, desiredPayload, `"formatVersion": 5`)
	require.NotContains(t, desiredPayload, `"footer"`)
	var outboxStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM integration_outbox
		WHERE operation_key=$1`, "projection:"+projectionKey).Scan(&outboxStatus))
	require.Equal(t, "pending", outboxStatus)
}

func TestReconcileConversationProgressCardsUsesTerminalRunState(t *testing.T) {
	db := discordDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id, name, enabled)
		VALUES ($1, 'Terminal Progress Test', true)`, testGuildID)
	require.NoError(t, err)
	seed := seedDiscordManagerData(t, db)
	var profileID, workspaceID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM agent_profiles
		WHERE name = 'Default'`).Scan(&profileID))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT workspace_id
		FROM discord_forums WHERE id = $1`, seed.workspaceForumID).Scan(&workspaceID))

	conversationID, controlID, intentID, runID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err = db.ExecContext(ctx, `INSERT INTO discord_conversations
		(id, guild_id, forum_id, thread_id, starter_message_id, owner_discord_user_id,
			workspace_project_id, agent_profile_id, title)
		VALUES ($1,$2,$3,'terminal-thread','terminal-starter','1001',$4,$5,'Terminal')`,
		conversationID, testGuildID, seed.workspaceForumID, seed.workspaceProjectID, profileID)
	require.NoError(t, err)
	sessionID := bindDiscordConversationSessionForTest(t, db, conversationID)
	_, err = db.ExecContext(ctx, `INSERT INTO codex_thread_controls
		(id, source_type, discord_conversation_id, session_id, workspace_project_id, agent_profile_id,
			worker_id, workspace_id)
		VALUES ($1,'workspace_session',$2,$3,$4,$5,$6,$7)`,
		controlID, conversationID, sessionID, seed.workspaceProjectID, profileID,
		seed.workerID, workspaceID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO codex_turn_intents
		(id, control_id, sequence_no, source_type, input_surface, discord_conversation_id, session_id,
			workspace_project_id, agent_profile_id, idempotency_key, status, finished_at)
		VALUES ($1,$2,1,'workspace_session','desktop',$3,$4,$5,$6,$7,'canceled',now())`,
		intentID, controlID, conversationID, sessionID, seed.workspaceProjectID, profileID,
		"terminal-progress-"+intentID.String())
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO codex_turn_runs
		(id, control_id, primary_intent_id, attempt, lease_owner, lease_epoch,
			capability_hash, status, finished_at)
		VALUES ($1,$2,$3,1,'desktop-app-server',1,$4,'canceled',now())`,
		runID, controlID, intentID, strings.Repeat("f", 64))
	require.NoError(t, err)

	historyKey := "conversation:" + conversationID.String() + ":message:history-input"
	_, err = db.ExecContext(ctx, `INSERT INTO discord_projections
		(guild_id, projection_key, resource_id, message_id, desired_payload)
		VALUES ($1,$2,'terminal-thread','history-message',$3)`, testGuildID,
		historyKey, mustJSON(map[string]any{
			"card": ComponentCardPayload{AccentColor: cardColorBlurple,
				Header: "Codex · 已引导对话"},
			"progress": conversationProgressPayload{
				FormatVersion: conversationProgressFormatVersion,
				RunID:         runID.String(), State: ConversationGuided,
				Summary: "引导前的历史动态。", Page: 0,
			},
		}))
	require.NoError(t, err)
	projectionKey := "conversation:" + conversationID.String() + ":message:desktop-input"
	_, err = db.ExecContext(ctx, `INSERT INTO discord_projections
		(guild_id, projection_key, resource_id, message_id, desired_payload)
		VALUES ($1,$2,'terminal-thread','terminal-message',$3)`, testGuildID,
		projectionKey, mustJSON(map[string]any{
			"card": ComponentCardPayload{AccentColor: cardColorBlurple,
				Header: "⚙️ Codex · 思考中", Body: "`42s` · `3 项动态`",
				Timeline: "> ↳ 已检查工作区\n\n正在整理结果。",
				Buttons:  []ComponentButtonPayload{{Label: "最新", CustomID: "progress-latest"}}},
			"progress": conversationProgressPayload{
				FormatVersion: conversationProgressFormatVersion,
				RunID:         runID.String(), State: ConversationRunning,
				Summary: "正在处理请求。", Page: 0,
			},
		}))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_turn_status_cards
		(run_id, guild_id, projection_key, revision, role)
		VALUES ($1,$2,$3,0,'history'), ($1,$2,$4,1,'current')`,
		runID, testGuildID, historyKey, projectionKey)
	require.NoError(t, err)

	require.NoError(t, ReconcileConversationProgressCards(ctx, db, testGuildID))
	var desiredPayload json.RawMessage
	require.NoError(t, db.QueryRowContext(ctx, `SELECT desired_payload
		FROM discord_projections WHERE guild_id=$1 AND projection_key=$2`,
		testGuildID, projectionKey).Scan(&desiredPayload))
	var desired struct {
		Card     ComponentCardPayload        `json:"card"`
		Progress conversationProgressPayload `json:"progress"`
	}
	require.NoError(t, json.Unmarshal(desiredPayload, &desired))
	require.Equal(t, ConversationCanceled, desired.Progress.State)
	require.Equal(t, "本轮已停止。", desired.Progress.Summary)
	require.Equal(t, "⏹️ Codex · 已停止", desired.Card.Header)
	require.Equal(t, "`42s` · `3 项动态`", desired.Card.Body)
	require.Equal(t, "> ↳ 已检查工作区\n\n正在整理结果。", desired.Card.Timeline)
	require.Equal(t, []ComponentButtonPayload{{Label: "最新", CustomID: "progress-latest"}}, desired.Card.Buttons)
	var historyVersion, currentVersion int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT desired_version
		FROM discord_projections WHERE guild_id=$1 AND projection_key=$2`,
		testGuildID, historyKey).Scan(&historyVersion))
	require.EqualValues(t, 1, historyVersion)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT desired_version
		FROM discord_projections WHERE guild_id=$1 AND projection_key=$2`,
		testGuildID, projectionKey).Scan(&currentVersion))
	require.EqualValues(t, 2, currentVersion)

	// 第二次重算必须保持幂等；历史卡仍为“已引导”，当前卡已是正确终态。
	require.NoError(t, ReconcileConversationProgressCards(ctx, db, testGuildID))
	var historyVersionAfter, currentVersionAfter int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT desired_version
		FROM discord_projections WHERE guild_id=$1 AND projection_key=$2`,
		testGuildID, historyKey).Scan(&historyVersionAfter))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT desired_version
		FROM discord_projections WHERE guild_id=$1 AND projection_key=$2`,
		testGuildID, projectionKey).Scan(&currentVersionAfter))
	require.Equal(t, historyVersion, historyVersionAfter)
	require.Equal(t, currentVersion, currentVersionAfter)
}

const (
	testGuildID = "100000000000000001"
	testBotID   = "100000000000000099"
)

type discordManagerSeed struct {
	administratorID          uuid.UUID
	workspaceForumID         uuid.UUID
	workspaceProjectID       uuid.UUID
	workspaceID              uuid.UUID
	workerID                 uuid.UUID
	workItemID               uuid.UUID
	codexCategoryID          string
	repositoryForumChannelID string
	workspaceForumChannelID  string
	repositoryID             uuid.UUID
	repositoryForumID        uuid.UUID
}

func seedDiscordManagerData(t *testing.T, db *sql.DB) discordManagerSeed {
	t.Helper()
	ctx := context.Background()
	seed := discordManagerSeed{
		codexCategoryID: "100000000000000011", workspaceForumChannelID: "100000000000000012",
		repositoryForumChannelID: "100000000000000022",
	}
	var workerID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workers
		(name, roles, status) VALUES ('discord-test-node', '["github","discord"]', 'online')
		RETURNING id`).Scan(&workerID))
	seed.workerID = workerID
	_, err := db.ExecContext(ctx, `INSERT INTO platform_settings(setting_key, value) VALUES
		('worker.default.github', jsonb_build_object('workerId', $1::text)),
		('worker.default.discord', jsonb_build_object('workerId', $1::text))`, workerID)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO administrators
		(username, password_hash, totp_secret_ciphertext) VALUES ('discord-admin', 'hash', $1) RETURNING id`,
		[]byte("secret")).Scan(&seed.administratorID))
	for _, user := range []struct{ id, login string }{{"1001", "alice"}, {"1002", "bob"}, {"1003", "charlie"}} {
		_, err := db.ExecContext(ctx, `INSERT INTO discord_members
			(guild_id, discord_user_id, username, display_name) VALUES ($1, $2, $3, $3)`, testGuildID, user.id, user.login)
		require.NoError(t, err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO discord_identity_bindings
		(guild_id, discord_user_id, github_user_id, github_login) VALUES ($1, '1001', 101, 'alice')`, testGuildID)
	require.NoError(t, err)

	categoryResource := insertDiscordResource(t, db, "category.codex.01", seed.codexCategoryID, "category", "Codex 会话 01", "")
	_ = categoryResource
	var installationID, repositoryID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO scm_installations
		(provider, external_id, account_login, account_type) VALUES ('github', 42, 'owner', 'Organization') RETURNING id`).Scan(&installationID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO repositories
		(installation_id, provider, external_id, owner, name, default_branch, clone_url)
		VALUES ($1, 'github', 43, 'owner', 'repo', 'main', 'https://example.invalid/repo.git') RETURNING id`, installationID).Scan(&repositoryID))
	seed.repositoryID = repositoryID
	repositoryResource := insertDiscordResource(t, db, "forum.repository."+repositoryID.String(),
		seed.repositoryForumChannelID, "forum", "owner-repo", "")
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_forums(guild_id, resource_id, forum_type, repository_id)
		VALUES ($1, $2, 'github', $3) RETURNING id`, testGuildID, repositoryResource, repositoryID).Scan(&seed.repositoryForumID))
	_, err = db.ExecContext(ctx, `INSERT INTO discord_forum_access(forum_id, discord_user_id, access_level)
		VALUES ($1, '1001', 'readonly')`, seed.repositoryForumID)
	require.NoError(t, err)
	var workspaceID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO worker_workspaces
		(guild_id, owner_discord_user_id, worker_id)
		VALUES ($1, '1001', $2) RETURNING id`, testGuildID, workerID).
		Scan(&workspaceID))
	seed.workspaceID = workspaceID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_projects
		(workspace_id,relative_path,name,project_kind,availability_status,
		 branch,head_sha,dirty,remote_url,last_seen_at)
		VALUES ($1,'workspaces/repo','repo','git','available',
			'main','seed-head',false,'https://example.invalid/repo.git',now())
		RETURNING id`, workspaceID).Scan(&seed.workspaceProjectID))
	workspaceResource := insertDiscordResource(t, db, "forum.workspace.seed", seed.workspaceForumChannelID,
		"forum", "dev-alice-repo", seed.codexCategoryID)
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_forums
		(guild_id,resource_id,forum_type,owner_discord_user_id,
		 workspace_project_id,workspace_id)
		VALUES ($1,$2,'workspace','1001',$3,$4) RETURNING id`,
		testGuildID, workspaceResource, seed.workspaceProjectID, workspaceID).
		Scan(&seed.workspaceForumID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO work_items
		(repository_id, kind, external_number, title) VALUES ($1, 'issue', 7, 'Needs help') RETURNING id`, repositoryID).Scan(&seed.workItemID))
	intentID := insertProjectionIntent(t, db, seed.workItemID, repositoryID, "projection-job", "alice")
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_intents SET status = 'failed',
		last_error_code = 'test_failure', last_error_message = 'test', finished_at = now() WHERE id = $1`, intentID)
	require.NoError(t, err)
	insertDiscordResource(t, db, "system.status", "100000000000000031", "text", "系统状态", "")
	insertDiscordResource(t, db, "system.alerts", "100000000000000032", "text", "系统告警", "")
	return seed
}

func insertProjectionIntent(t *testing.T, db *sql.DB, workItemID, repositoryID uuid.UUID,
	key, actor string,
) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var profileID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM agent_profiles WHERE name = 'Default'`).Scan(&profileID))
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	intentID, inserted, err := codexcontrol.NewRepository(db, time.Minute).Enqueue(ctx, tx,
		codexcontrol.EnqueueRequest{
			SourceType: codexcontrol.SourceGitHub, WorkItemID: workItemID,
			RepositoryID: repositoryID, AgentProfileID: profileID,
			IdempotencyKey: key, Instruction: "test", ActorLogin: actor, ReplyPolicy: "required",
		})
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, tx.Commit())
	return intentID
}

func insertDiscordResource(t *testing.T, db *sql.DB, key, discordID, kind, name, parentID string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, db.QueryRow(`INSERT INTO discord_resources
		(guild_id, resource_key, discord_id, kind, parent_discord_id, name, managed_marker)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7) RETURNING id`,
		testGuildID, key, discordID, kind, parentID, name, managedMarker(key)).Scan(&id))
	return id
}

func completeOutboxForTest(t *testing.T, ctx context.Context, db *sql.DB, key string, response json.RawMessage) {
	t.Helper()
	var item OutboxItem
	var id uuid.UUID
	item.LeaseToken = strings.Repeat("a", 64)
	require.NoError(t, db.QueryRowContext(ctx, `UPDATE integration_outbox SET status='sending',
		inflight_revision=request_revision, inflight_operation_type=operation_type,
		inflight_route_key=route_key, inflight_payload=payload, inflight_nonce=nonce,
		lease_token=$2, lease_expires_at=now()+interval '1 minute'
		WHERE operation_key = $1 RETURNING id, operation_key, operation_type, route_key, payload,
		COALESCE(nonce, ''), attempt_count, max_attempts, request_revision`, key, item.LeaseToken).
		Scan(&id, &item.OperationKey, &item.OperationType, &item.RouteKey, &item.Payload,
			&item.Nonce, &item.Attempt, &item.MaxAttempts, &item.RequestRevision))
	item.ID = id.String()
	store := NewSQLoutbox(db)
	require.NoError(t, store.RecordDelivery(ctx, &item, response))
	require.NoError(t, store.Apply(ctx, item))
}

func testGatewayHandlers(t *testing.T, ctx context.Context, db *sql.DB, manager *Manager, seed discordManagerSeed) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/channels/"+seed.workspaceForumChannelID+"/threads":
			_, _ = response.Write([]byte(fmt.Sprintf(`{"id":"2010","guild_id":%q,"parent_id":%q,"type":11,"name":"Codex 正在生成标题","owner_id":%q,"message_count":1,"member_count":1,"rate_limit_per_user":0,"thread_metadata":{"archived":false,"auto_archive_duration":10080,"archive_timestamp":"2026-07-18T00:00:00Z","locked":false},"message":{"id":"3011","channel_id":"2010","author":{"id":%q,"username":"bot","discriminator":"0","bot":true},"content":"bot-created task"}}`,
				testGuildID, seed.workspaceForumChannelID, testBotID, testBotID)))
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/channels/"):
			threadID := strings.TrimPrefix(request.URL.Path, "/channels/")
			if threadID == "2099" {
				_, _ = response.Write([]byte(fmt.Sprintf(`{"id":%q,"guild_id":%q,"parent_id":"2999","type":0,"name":"general","position":0,"permission_overwrites":[],"rate_limit_per_user":0,"nsfw":false}`, threadID, testGuildID)))
				return
			}
			_, _ = response.Write([]byte(fmt.Sprintf(`{"id":%q,"guild_id":%q,"parent_id":%q,"type":11,"name":"Conversation","owner_id":"1001","message_count":1,"member_count":1,"rate_limit_per_user":0,"thread_metadata":{"archived":false,"auto_archive_duration":10080,"archive_timestamp":"2026-07-18T00:00:00Z","locked":false}}`,
				threadID, testGuildID, seed.workspaceForumChannelID)))
		case request.Method == http.MethodPatch && strings.Contains(request.URL.Path, "/messages/@original"):
			_, _ = response.Write([]byte(`{"id":"9901","channel_id":"2001","content":"updated"}`))
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	remote := NewDisgoRemote("token", server.URL, server.Client())
	t.Cleanup(func() { remote.Close(context.Background()) })
	client := &bot.Client{ApplicationID: snowflake.ID(900), Rest: remote.rest}
	conversationService := NewConversationService(db)
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	bindingService := NewBindingService(NewSQLBindingStore(db), box, fakeOAuthApp{},
		"https://tyr.example", "https://api.github.com")
	connector := NewDisgoConnector(manager, conversationService, bindingService,
		testGuildID, "token", zap.NewNop())
	testCodexConfigurationInteractions(t, ctx, db, connector, client, seed)

	messageEvent := newMessageEvent(t, client, "2001", "3001", "first message")
	nickname := "Alice Operator"
	contentType := "text/plain"
	messageEvent.Message.Member = &discord.Member{Nick: &nickname}
	messageEvent.Message.Attachments = []discord.Attachment{{
		ID: snowflake.ID(4001), Filename: "notes.txt", ContentType: &contentType,
		Size: 12, URL: "https://cdn.discordapp.com/attachments/2001/4001/notes.txt",
	}}
	connector.onMessage(messageEvent)
	connector.onMessage(messageEvent)
	var conversationID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM discord_conversations
		WHERE guild_id = $1 AND thread_id = '2001'`, testGuildID).Scan(&conversationID))
	var eventCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_inbound_events
		WHERE event_id = 'message:3001'`).Scan(&eventCount))
	require.Equal(t, 1, eventCount)
	var displayName string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT display_name FROM discord_members
		WHERE guild_id = $1 AND discord_user_id = '1001'`, testGuildID).Scan(&displayName))
	require.Equal(t, nickname, displayName)
	var attachmentKind, attachmentMediaType string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT kind, media_type FROM discord_attachments
		WHERE message_id = '3001' AND discord_attachment_id = '4001'`).
		Scan(&attachmentKind, &attachmentMediaType))
	require.Equal(t, "file", attachmentKind)
	require.Equal(t, contentType, attachmentMediaType)
	messageUpdate := newMessageUpdateEvent(t, client, "2001", "3001", "first message edited")
	connector.onMessageUpdate(messageUpdate)
	connector.onMessageUpdate(messageUpdate)
	var editedBody string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT body FROM discord_input_messages
		WHERE conversation_id=$1 AND message_id='3001'`, conversationID).Scan(&editedBody))
	require.Equal(t, "first message edited", editedBody)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_inbound_events
		WHERE event_id LIKE 'message-update:3001:%'`).Scan(&eventCount))
	require.Equal(t, 1, eventCount)

	normalMessage := newMessageEvent(t, client, "2099", "3099", "普通频道消息")
	connector.onMessage(normalMessage)
	var normalEventStatus string
	var normalEventError sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status, error FROM discord_inbound_events
		WHERE event_id = 'message:3099'`).Scan(&normalEventStatus, &normalEventError))
	require.Equal(t, "processed", normalEventStatus)
	require.False(t, normalEventError.Valid)
	var normalConversationCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_conversations
		WHERE guild_id = $1 AND thread_id = '2099'`, testGuildID).Scan(&normalConversationCount))
	require.Zero(t, normalConversationCount)

	var conversationStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM discord_conversations WHERE id = $1`, conversationID).Scan(&conversationStatus))
	require.Equal(t, "awaiting_configuration", conversationStatus)
	var queuedBeforeConfiguration int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_intents
		WHERE discord_conversation_id = $1`, conversationID).Scan(&queuedBeforeConfiguration))
	require.Zero(t, queuedBeforeConfiguration)
	_, err = conversationService.ConversationMode(ctx, testGuildID, "2001", "1003")
	require.Error(t, err, "新 Post 参数只能由创建者操作")
	var configurationDeadline sql.NullTime
	require.NoError(t, db.QueryRowContext(ctx, `SELECT configuration_deadline
		FROM discord_conversations WHERE id = $1`, conversationID).Scan(&configurationDeadline))
	require.False(t, configurationDeadline.Valid)
	configurationState, err := conversationService.ConversationMode(ctx, testGuildID, "2001", "1001")
	require.NoError(t, err)
	configurationUpdate, err := conversationService.SetRuntimePreferences(ctx, testGuildID, "2001",
		"1001", conversationID, configurationState.SettingsRevision,
		ConversationConfiguration{Model: "gpt-5.6-terra", ReasoningEffort: "high",
			ServiceTier: "fast"})
	require.NoError(t, err)
	require.NoError(t, conversationService.FinalizeConfiguration(ctx, conversationID, "1001"))
	require.Len(t, configurationUpdate.Changes, 3)
	var configuredModel, configuredEffort, configuredTier string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status, model, reasoning_effort, service_tier
		FROM discord_conversations WHERE id = $1`, conversationID).
		Scan(&conversationStatus, &configuredModel, &configuredEffort, &configuredTier))
	require.Equal(t, "active", conversationStatus)
	require.Equal(t, "gpt-5.6-terra", configuredModel)
	require.Equal(t, "high", configuredEffort)
	require.Equal(t, "fast", configuredTier)
	attachmentRoot := t.TempDir()
	attachmentPath := filepath.Join(attachmentRoot, "stored", "notes.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(attachmentPath), 0o700))
	require.NoError(t, os.WriteFile(attachmentPath, []byte("stored notes"), 0o600))
	_, err = db.ExecContext(ctx, `UPDATE discord_attachments SET status = 'ready',
		storage_key = 'stored/notes.txt', stored_at = now() - interval '8 days'
		WHERE message_id = '3001' AND discord_attachment_id = '4001'`)
	require.NoError(t, err)
	conversationService.ConfigureAttachmentStore(attachmentRoot)
	require.NoError(t, conversationService.CleanupAttachments(ctx))
	require.FileExists(t, attachmentPath, "排队或运行中的附件不能清理")

	command := newCommandEvent(t, client, "5002", "2001", "codex", "stop")
	connector.onCommand(command)
	connector.onCommand(newCommandEvent(t, client, "5012", "2001", "github", "bind"))
	connector.onCommand(newCommandEvent(t, client, "5013", "2001", "github", "unbind"))
	connector.onCommand(newCommandEvent(t, client, "5014", "2001", "unknown", "command"))
	var jobStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM codex_turn_intents
		WHERE discord_conversation_id = $1 AND operation = 'turn_input'`, conversationID).Scan(&jobStatus))
	require.Equal(t, "canceled", jobStatus)
	var inputStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM discord_input_messages
		WHERE conversation_id = $1`, conversationID).Scan(&inputStatus))
	require.Equal(t, "canceled", inputStatus)
	require.NoError(t, conversationService.CleanupAttachments(ctx))
	require.NoFileExists(t, attachmentPath)
	var cleanedAttachmentStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM discord_attachments
		WHERE message_id = '3001' AND discord_attachment_id = '4001'`).
		Scan(&cleanedAttachmentStatus))
	require.Equal(t, "deleted", cleanedAttachmentStatus)

	messageEvent = newMessageEvent(t, client, "2002", "3002", "repository message")
	connector.onMessage(messageEvent)
	var repositoryConversationID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM discord_conversations
		WHERE guild_id = $1 AND thread_id = '2002'`, testGuildID).Scan(&repositoryConversationID))
	var selectedRepository sql.NullString
	var selectedProject uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT repository_id::text,
		workspace_project_id FROM discord_conversations
		WHERE id=$1`, repositoryConversationID).
		Scan(&selectedRepository, &selectedProject))
	require.False(t, selectedRepository.Valid)
	require.Equal(t, seed.workspaceProjectID, selectedProject)

	connector.onComponent(newComponentEvent(t, client, "5005", "2001", "github-unbind-confirm:1001", nil))
	var activeBinding int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_identity_bindings
		WHERE guild_id = $1 AND discord_user_id = '1001' AND status = 'active'`, testGuildID).Scan(&activeBinding))
	require.Zero(t, activeBinding)

	messageEvent = newMessageEvent(t, client, "2003", "3003", "created before binding")
	connector.onMessage(messageEvent)
	var unboundBinding sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `SELECT m.github_binding_id::text
		FROM discord_input_messages m JOIN discord_conversations c ON c.id = m.conversation_id
		WHERE c.guild_id = $1 AND c.thread_id = '2003'`, testGuildID).Scan(&unboundBinding))
	require.False(t, unboundBinding.Valid)
	_, err = bindingService.store.Bind(ctx, Binding{
		GuildID: testGuildID, DiscordUserID: "1001", GitHubUserID: 101, GitHubLogin: "alice",
	})
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT m.github_binding_id::text
		FROM discord_input_messages m JOIN discord_conversations c ON c.id = m.conversation_id
		WHERE c.guild_id = $1 AND c.thread_id = '2003'`, testGuildID).Scan(&unboundBinding))
	require.False(t, unboundBinding.Valid, "历史消息的身份快照不能被后续绑定追溯提升")

	_, err = db.ExecContext(ctx, `UPDATE codex_thread_controls SET status = 'error'
		WHERE discord_conversation_id = $1`, conversationID)
	require.NoError(t, err)
	connector.onMessage(newMessageEvent(t, client, "2001", "3010", "retry after terminal error"))
	var rejectedMessageCount, rejectionOutboxCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_input_messages
		WHERE message_id = '3010'`).Scan(&rejectedMessageCount))
	require.Zero(t, rejectedMessageCount)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM integration_outbox
		WHERE operation_key = 'conversation:terminated-rejection:3010'`).Scan(&rejectionOutboxCount))
	require.Equal(t, 1, rejectionOutboxCount)
}

func testCodexConfigurationInteractions(t *testing.T, ctx context.Context, db *sql.DB,
	connector *DisgoConnector, client *bot.Client, seed discordManagerSeed,
) {
	t.Helper()

	connector.onComponent(newComponentEvent(t, client, "5100", seed.workspaceForumChannelID,
		"codex-new-open", nil))
	modal, err := connector.newCodexModal(ctx, seed.workspaceForumChannelID, "1001", "default")
	require.NoError(t, err)
	require.Equal(t, newCodexModalPrefix+seed.workspaceForumChannelID+":default", modal.CustomID)
	require.Len(t, modal.Components, 5)
	_, _, _, _, err = connector.authorizedForum(ctx, seed.workspaceForumChannelID, "1003")
	require.NoError(t, err)
	_, _, _, _, err = connector.authorizedForum(ctx, seed.workspaceForumChannelID, "1002")
	require.Error(t, err)
	_, _, _, _, err = connector.authorizedForum(ctx, "999999999999999999", "1001")
	require.Error(t, err)
	_, err = connector.newCodexModal(ctx, seed.workspaceForumChannelID, "1002", "default")
	require.Error(t, err)
	models := []codexcatalog.Model{
		{ID: "codex-model", IsDefault: true,
			SupportedReasoningEfforts: []codexcatalog.ReasoningEffort{{ReasoningEffort: "xhigh"}},
			ServiceTiers:              []codexcatalog.ServiceTier{{ID: "priority"}}},
		{ID: "standard-model"},
	}
	options, custom := modelModalOptions("private-model", models)
	require.Len(t, options, 4)
	require.Equal(t, "private-model", custom.Value)
	require.NotEmpty(t, effortModalSelect("xhigh", []string{"low", "xhigh"}).Options)
	require.Len(t, tierModalSelect("fast", "codex-model", models, "速度").Options, 2)
	require.Len(t, tierModalSelect("fast", "standard-model", models, "速度").Options, 1)
	require.NoError(t, validateKnownModelSelection("codex-model", "xhigh", "fast", models))
	require.ErrorContains(t,
		validateKnownModelSelection("standard-model", "xhigh", "fast", models), "不支持快速模式")
	require.Empty(t, firstModalValue(nil))

	connector.onMessage(newMessageEvent(t, client, "2011", "3012", "edit configuration"))
	editID := conversationIDForThread(t, ctx, db, "2011")
	editState, err := connector.conversations.ConversationMode(ctx, testGuildID, "2011", "1001")
	require.NoError(t, err)
	staleStartButton := configurationStartButtonID(editState)
	connector.onComponent(newComponentEvent(t, client, "5101", "2011",
		runtimePreferencesButtonID(editState), nil))
	var status string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT configuration_status FROM discord_conversations
		WHERE id = $1`, editID).Scan(&status))
	require.Equal(t, "awaiting", status)

	configurationSubmit := newModalEvent(t, client, "5102", "2011",
		fmt.Sprintf("%s%s:%d", runtimeConfigurationModalPrefix, editID, editState.SettingsRevision),
		[]discord.LayoutComponent{
			discord.NewLabel("模型", discord.StringSelectMenuComponent{CustomID: "model", Values: []string{"__custom__"}}),
			discord.NewLabel("自定义模型", discord.TextInputComponent{CustomID: "custom_model", Value: "private-model"}),
			discord.NewLabel("思考等级", discord.StringSelectMenuComponent{CustomID: "reasoning_effort", Values: []string{"xhigh"}}),
			discord.NewLabel("速度", discord.StringSelectMenuComponent{CustomID: "service_tier", Values: []string{"fast"}}),
		})
	connector.onModalSubmit(configurationSubmit)
	connector.onModalSubmit(configurationSubmit)
	connector.onComponent(newComponentEvent(t, client, "5110", "2011", staleStartButton, nil))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT configuration_status
		FROM discord_conversations WHERE id = $1`, editID).Scan(&status))
	require.Equal(t, "awaiting", status, "过期等待卡只能刷新，不能启动会话")
	editState, err = connector.conversations.ConversationMode(ctx, testGuildID, "2011", "1001")
	require.NoError(t, err)
	connector.onComponent(newComponentEvent(t, client, "5111", "2011",
		modeButtonID(editState, "plan"), nil))
	var model, effort, tier, collaborationMode string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT configuration_status, model, reasoning_effort,
		service_tier, collaboration_mode FROM discord_conversations WHERE id = $1`, editID).
		Scan(&status, &model, &effort, &tier, &collaborationMode))
	require.Equal(t, "awaiting", status)
	require.Equal(t, "private-model", model)
	require.Equal(t, "xhigh", effort)
	require.Equal(t, "fast", tier)
	require.Equal(t, "plan", collaborationMode)
	editState, err = connector.conversations.ConversationMode(ctx, testGuildID, "2011", "1001")
	require.NoError(t, err)
	connector.onComponent(newComponentEvent(t, client, "5112", "2011",
		configurationStartButtonID(editState), nil))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT configuration_status
		FROM discord_conversations WHERE id = $1`, editID).Scan(&status))
	require.Equal(t, "configured", status)

	connector.onMessage(newMessageEvent(t, client, "2012", "3013", "start defaults"))
	startID := conversationIDForThread(t, ctx, db, "2012")
	startState, err := connector.conversations.ConversationMode(ctx, testGuildID, "2012", "1001")
	require.NoError(t, err)
	connector.onComponent(newComponentEvent(t, client, "5103", "2012",
		configurationStartButtonID(startState), nil))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT configuration_status FROM discord_conversations
		WHERE id = $1`, startID).Scan(&status))
	require.Equal(t, "configured", status)
	connector.onComponent(newComponentEvent(t, client, "5105", "2012", "codex-config-start:bad-id", nil))

	connector.onMessage(newMessageEvent(t, client, "2013", "3014", "wait for confirmation"))
	waitingID := conversationIDForThread(t, ctx, db, "2013")
	var waitingStatus string
	var configurationDeadline sql.NullTime
	require.NoError(t, db.QueryRowContext(ctx, `SELECT configuration_status,
		configuration_deadline FROM discord_conversations WHERE id = $1`, waitingID).
		Scan(&waitingStatus, &configurationDeadline))
	require.Equal(t, "awaiting", waitingStatus)
	require.False(t, configurationDeadline.Valid)

	newPostSubmit := newModalEvent(t, client, "5104", seed.workspaceForumChannelID,
		newCodexModalPrefix+seed.workspaceForumChannelID+":plan", []discord.LayoutComponent{
			discord.NewLabel("任务", discord.TextInputComponent{CustomID: "task", Value: "bot-created task"}),
			discord.NewLabel("模型", discord.StringSelectMenuComponent{CustomID: "model", Values: []string{"gpt-5.6-sol"}}),
			discord.NewLabel("自定义模型", discord.TextInputComponent{CustomID: "custom_model"}),
			discord.NewLabel("服务等级", discord.StringSelectMenuComponent{CustomID: "service_tier", Values: []string{"standard"}}),
			discord.NewLabel("思考等级", discord.StringSelectMenuComponent{CustomID: "reasoning_effort", Values: []string{"medium"}}),
		})
	connector.onModalSubmit(newPostSubmit)
	createdID := conversationIDForThread(t, ctx, db, "2010")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT configuration_status, title_rename_status,
		collaboration_mode FROM discord_conversations WHERE id = $1`, createdID).
		Scan(&status, &model, &collaborationMode))
	require.Equal(t, "configured", status)
	require.Equal(t, "pending", model)
	require.Equal(t, "plan", collaborationMode)
	emptyCustom := newModalEvent(t, client, "5107", seed.workspaceForumChannelID,
		newCodexModalPrefix+seed.workspaceForumChannelID+":default", []discord.LayoutComponent{
			discord.NewLabel("任务", discord.TextInputComponent{CustomID: "task", Value: "invalid custom model"}),
			discord.NewLabel("模型", discord.StringSelectMenuComponent{CustomID: "model", Values: []string{"__custom__"}}),
			discord.NewLabel("自定义模型", discord.TextInputComponent{CustomID: "custom_model"}),
			discord.NewLabel("服务等级", discord.StringSelectMenuComponent{CustomID: "service_tier", Values: []string{"standard"}}),
			discord.NewLabel("思考等级", discord.StringSelectMenuComponent{CustomID: "reasoning_effort", Values: []string{"__default__"}}),
		})
	connector.onModalSubmit(emptyCustom)
	emptyTask := newModalEvent(t, client, "5108", seed.workspaceForumChannelID,
		newCodexModalPrefix+seed.workspaceForumChannelID+":default", []discord.LayoutComponent{
			discord.NewLabel("任务", discord.TextInputComponent{CustomID: "task"}),
			discord.NewLabel("模型", discord.StringSelectMenuComponent{CustomID: "model", Values: []string{"__default__"}}),
			discord.NewLabel("自定义模型", discord.TextInputComponent{CustomID: "custom_model"}),
			discord.NewLabel("服务等级", discord.StringSelectMenuComponent{CustomID: "service_tier", Values: []string{"standard"}}),
			discord.NewLabel("思考等级", discord.StringSelectMenuComponent{CustomID: "reasoning_effort", Values: []string{"low"}}),
		})
	connector.onModalSubmit(emptyTask)
	connector.onModalSubmit(newModalEvent(t, client, "5109", "2012", "unrelated-modal", nil))
	_, err = db.ExecContext(ctx, `UPDATE discord_conversations SET title_rename_status = 'skipped'
		WHERE title_rename_status = 'pending' AND id <> $1`, createdID)
	require.NoError(t, err)
	generator := &TitleGenerator{db: db}
	_, err = generator.claim(ctx, "pending")
	require.ErrorIs(t, err, sql.ErrNoRows,
		"公共 Session 标题任务必须阻止旧 fallback Outbox 与 Luna 结果竞争")
	var titleTaskStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT task.status
		FROM workspace_session_title_tasks task
		JOIN discord_conversations conversation ON conversation.session_id=task.session_id
		WHERE conversation.id=$1`, createdID).Scan(&titleTaskStatus))
	require.Equal(t, "pending", titleTaskStatus)
	_, err = db.ExecContext(ctx, `DELETE FROM workspace_session_title_tasks
		WHERE session_id=(SELECT session_id FROM discord_conversations WHERE id=$1)`, createdID)
	require.NoError(t, err)
	claimedTitle, err := generator.claim(ctx, "pending")
	require.NoError(t, err)
	require.Equal(t, createdID, claimedTitle.ID)
	require.NoError(t, generator.schedule(ctx, claimedTitle, "  Historical\nFallback  "))
	completeOutboxForTest(t, ctx, db, "conversation-title:"+createdID.String(), nil)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT title_rename_status FROM discord_conversations
		WHERE id = $1`, createdID).Scan(&status))
	require.Equal(t, "completed", status)
	var generated string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT generated_title FROM discord_conversations
		WHERE id = $1`, createdID).Scan(&generated))
	require.Equal(t, "Historical Fallback", generated)
	for _, value := range []struct{ effort, tier string }{
		{"low", "standard"}, {"medium", "fast"}, {"high", "standard"}, {"xhigh", "fast"}, {"unknown", "standard"},
	} {
		card := conversationModeCard(ConversationModeState{ConversationID: uuid.New(),
			Mode: "default", TriggerMode: "interactive", ReasoningEffort: value.effort,
			ServiceTier: value.tier, Awaiting: true}, "")
		require.Contains(t, card.Body, "**模型**")
		require.Contains(t, card.Body, "**速度**")
		require.Contains(t, card.Body, "**思考等级**")
	}
}

func conversationIDForThread(t *testing.T, ctx context.Context, db *sql.DB, threadID string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM discord_conversations
		WHERE guild_id = $1 AND thread_id = $2`, testGuildID, threadID).Scan(&id))
	return id
}

func newModalEvent(t *testing.T, client *bot.Client, id, channelID, customID string,
	components []discord.LayoutComponent,
) *events.ModalSubmitInteractionCreate {
	t.Helper()
	raw := fmt.Sprintf(`{"id":%q,"application_id":"900","type":5,"token":%q,"version":1,"guild_id":%q,"channel":{"id":%q,"type":11,"name":"Conversation"},"member":{"user":{"id":"1001","username":"alice","discriminator":"0"},"roles":[]},"locale":"en-US","guild_locale":"en-US","data":{"custom_id":%q,"components":[]}}`,
		id, "token-"+id, testGuildID, channelID, customID)
	var interaction discord.ModalSubmitInteraction
	require.NoError(t, json.Unmarshal([]byte(raw), &interaction))
	interaction.Data.Components = components
	return &events.ModalSubmitInteractionCreate{
		GenericEvent: events.NewGenericEvent(client, 4, 0), ModalSubmitInteraction: interaction,
		Respond: func(discord.InteractionResponseType, discord.InteractionResponseData, ...disgorest.RequestOpt) error {
			return nil
		},
	}
}

func newMessageEvent(t *testing.T, client *bot.Client, threadID, messageID, content string) *events.MessageCreate {
	t.Helper()
	guildID, err := snowflake.Parse(testGuildID)
	require.NoError(t, err)
	channelID, err := snowflake.Parse(threadID)
	require.NoError(t, err)
	id, err := snowflake.Parse(messageID)
	require.NoError(t, err)
	return &events.MessageCreate{GenericMessage: &events.GenericMessage{
		GenericEvent: events.NewGenericEvent(client, 1, 0), MessageID: id, ChannelID: channelID, GuildID: &guildID,
		Message: discord.Message{ID: id, ChannelID: channelID, Content: content,
			Author: discord.User{ID: snowflake.ID(1001), Username: "alice", Discriminator: "0"}},
	}}
}

func newMessageUpdateEvent(t *testing.T, client *bot.Client, threadID, messageID,
	content string,
) *events.MessageUpdate {
	t.Helper()
	created := newMessageEvent(t, client, threadID, messageID, content)
	editedAt := time.Now().UTC()
	created.Message.EditedTimestamp = &editedAt
	return &events.MessageUpdate{GenericMessage: created.GenericMessage}
}

func newCommandEvent(t *testing.T, client *bot.Client, id, channelID, command, subcommand string) *events.ApplicationCommandInteractionCreate {
	t.Helper()
	raw := fmt.Sprintf(`{"id":%q,"application_id":"900","type":2,"token":%q,"version":1,"guild_id":%q,"channel":{"id":%q,"type":11,"name":"Conversation"},"member":{"user":{"id":"1001","username":"alice","discriminator":"0"},"roles":[]},"locale":"en-US","guild_locale":"en-US","data":{"id":"901","name":%q,"type":1,"options":[{"name":%q,"type":1,"options":[]}]}}`,
		id, "token-"+id, testGuildID, channelID, command, subcommand)
	var interaction discord.ApplicationCommandInteraction
	require.NoError(t, json.Unmarshal([]byte(raw), &interaction))
	return &events.ApplicationCommandInteractionCreate{
		GenericEvent: events.NewGenericEvent(client, 2, 0), ApplicationCommandInteraction: interaction,
		Respond: func(discord.InteractionResponseType, discord.InteractionResponseData, ...disgorest.RequestOpt) error {
			return nil
		},
	}
}

func newComponentEvent(t *testing.T, client *bot.Client, id, channelID, customID string, values []string) *events.ComponentInteractionCreate {
	t.Helper()
	componentType := 2
	data := fmt.Sprintf(`{"component_type":2,"custom_id":%q}`, customID)
	if values != nil {
		componentType = 3
		encoded, err := json.Marshal(map[string]any{"component_type": componentType, "custom_id": customID, "values": values})
		require.NoError(t, err)
		data = string(encoded)
	}
	raw := fmt.Sprintf(`{"id":%q,"application_id":"900","type":3,"token":%q,"version":1,"guild_id":%q,"channel":{"id":%q,"type":11,"name":"Conversation"},"member":{"user":{"id":"1001","username":"alice","discriminator":"0"},"roles":[]},"locale":"en-US","guild_locale":"en-US","data":%s,"message":{"id":"8001","channel_id":%q,"author":{"id":"900","username":"bot","discriminator":"0","bot":true},"content":"action"}}`,
		id, "token-"+id, testGuildID, channelID, data, channelID)
	var interaction discord.ComponentInteraction
	require.NoError(t, json.Unmarshal([]byte(raw), &interaction))
	return &events.ComponentInteractionCreate{
		GenericEvent: events.NewGenericEvent(client, 3, 0), ComponentInteraction: interaction,
		Respond: func(discord.InteractionResponseType, discord.InteractionResponseData, ...disgorest.RequestOpt) error {
			return nil
		},
	}
}

func testDiscordRecoveryOrchestration(t *testing.T, ctx context.Context, db *sql.DB, manager *Manager, seed discordManagerSeed) {
	t.Helper()
	store := NewSQLBindingStore(db)
	state := OAuthState{GuildID: testGuildID, DiscordUserID: "1002", VerifierCiphertext: []byte("cipher"), VerifierNonce: []byte("nonce")}
	require.NoError(t, store.SaveOAuthState(ctx, "state-hash", state, time.Now().Add(time.Minute)))
	consumed, err := store.ConsumeOAuthState(ctx, "state-hash", time.Now())
	require.NoError(t, err)
	require.Equal(t, state, consumed)
	_, err = store.ConsumeOAuthState(ctx, "state-hash", time.Now())
	require.Error(t, err)
	_, err = store.Bind(ctx, Binding{GuildID: testGuildID, DiscordUserID: "1001", GitHubUserID: 101, GitHubLogin: "alice"})
	require.NoError(t, err)
	updatedBinding, err := store.Bind(ctx, Binding{
		GuildID: testGuildID, DiscordUserID: "1001", GitHubUserID: 101, GitHubLogin: "alice",
	})
	require.NoError(t, err)
	require.Equal(t, "alice", updatedBinding.GitHubLogin)
	_, err = store.Bind(ctx, Binding{
		GuildID: testGuildID, DiscordUserID: "1001", GitHubUserID: 999, GitHubLogin: "other",
	})
	require.Error(t, err)
	_, err = store.Bind(ctx, Binding{
		GuildID: testGuildID, DiscordUserID: "1002", GitHubUserID: 101, GitHubLogin: "alice",
	})
	require.Error(t, err)
	current, err := store.CurrentBinding(ctx, testGuildID, "1001")
	require.NoError(t, err)
	require.Equal(t, "alice", current.GitHubLogin)

	appManager := ghadapter.NewManager(db, manager.secrets)
	_, _, err = NewGitHubOAuthApp(appManager).Credentials(ctx)
	require.Error(t, err)
	daemon := NewDaemon(manager, NewConversationService(db), &BindingService{store: store}, appManager,
		nil, zap.NewNop())
	_, err = daemon.githubPermission(ctx, 1, "owner", "repo", "alice")
	require.Error(t, err)
	defaultRemote := daemon.newRemote("token", "http://127.0.0.1")
	defaultRemote.Close(ctx)
	require.NotNil(t, daemon.newGateway(readySettingsForTest(), "token"))
	readySettings, readyToken, err := daemon.waitUntilEnabled(ctx)
	require.NoError(t, err)
	require.Equal(t, testGuildID, readySettings.GuildID)
	require.Equal(t, "test-token", readyToken)
	daemon.refreshAllProjections(ctx, testGuildID, &projectionRemote{guild: RemoteGuild{ID: testGuildID}})
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, daemon.runBackground(canceled, testGuildID, &projectionRemote{}), context.Canceled)
	forums, err := daemon.repositoryForums(ctx, testGuildID)
	require.NoError(t, err)
	require.Len(t, forums, 1)
	require.Equal(t, int64(43), forums[0].RepositoryExternalID)
	permissionChecks := 0
	daemon.githubPermission = func(context.Context, int64, string, string, string) (string, error) {
		permissionChecks++
		return "read", nil
	}
	require.NoError(t, daemon.handleRepositoryPermissionSync(ctx, testGuildID,
		`{"installationId":42,"repositoryIds":[999]}`))
	require.Zero(t, permissionChecks)
	require.NoError(t, daemon.handleRepositoryPermissionSync(ctx, testGuildID,
		`{"installationId":42,"repositoryIds":[43]}`))
	require.Equal(t, 1, permissionChecks)
	permissionChecks = 0
	require.NoError(t, daemon.handleRepositoryPermissionSync(ctx, testGuildID,
		`{"discordUserId":"1001"}`))
	require.Equal(t, 1, permissionChecks)
	require.Error(t, daemon.handleRepositoryPermissionSync(ctx, testGuildID, `{`))
	require.Error(t, daemon.handleRepositoryPermissionSync(ctx, testGuildID, `{}`))
	var repositoryAccess int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_forum_access
		WHERE forum_id = $1 AND discord_user_id = '1001'`, forums[0].ForumID).Scan(&repositoryAccess))
	require.Equal(t, 1, repositoryAccess)
	daemon.githubPermission = func(context.Context, int64, string, string, string) (string, error) {
		return "none", nil
	}
	require.NoError(t, daemon.syncRepositoryPermissions(ctx, testGuildID))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_forum_access
		WHERE forum_id = $1 AND discord_user_id = '1001'`, forums[0].ForumID).Scan(&repositoryAccess))
	require.Zero(t, repositoryAccess)
	require.NoError(t, db.QueryRowContext(ctx, `UPDATE repositories SET enabled = false WHERE id = $1 RETURNING enabled`,
		seed.repositoryID).Scan(&forums[0].Enabled))
	permissionChecks = 0
	daemon.githubPermission = func(context.Context, int64, string, string, string) (string, error) {
		permissionChecks++
		return "read", nil
	}
	require.NoError(t, daemon.handleRepositoryPermissionSync(ctx, testGuildID,
		`{"installationId":42,"repositoryIds":[43]}`))
	require.Zero(t, permissionChecks)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_forum_access
		WHERE forum_id = $1 AND discord_user_id = '1001'`, forums[0].ForumID).Scan(&repositoryAccess))
	require.Zero(t, repositoryAccess)
	_, err = db.ExecContext(ctx, `UPDATE repositories SET enabled = true WHERE id = $1`, seed.repositoryID)
	require.NoError(t, err)
	require.NoError(t, manager.syncRepositoryForumPermissions(ctx, testGuildID, forums[0]))
	require.Greater(t, repositoryPermissionRank("admin"), repositoryPermissionRank("read"))
	require.Greater(t, repositoryPermissionRank("maintain"), repositoryPermissionRank("write"))
	require.Greater(t, repositoryPermissionRank("write"), repositoryPermissionRank("triage"))
	require.Equal(t, repositoryPermissionRank("read"), repositoryPermissionRank("pull"))
	require.Zero(t, repositoryPermissionRank("none"))

	actionRemote := &initializationActionRemote{}
	var projectionCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_projections WHERE guild_id = $1`, testGuildID).
		Scan(&projectionCount))
	require.Positive(t, projectionCount)
	resetResult, err := manager.executeInitializationAction(ctx, testGuildID,
		InitializationAction{Kind: "projection.reset"}, actionRemote)
	require.NoError(t, err)
	require.EqualValues(t, projectionCount, resetResult["deleted"])
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_projections WHERE guild_id = $1`, testGuildID).
		Scan(&projectionCount))
	require.Zero(t, projectionCount)
	rulesID := "100000000000000041"
	updatesID := "100000000000000042"
	insertDiscordResource(t, db, "system.rules", rulesID, "text", "规则", "")
	insertDiscordResource(t, db, "system.updates", updatesID, "text", "更新", "")
	_, err = manager.executeInitializationAction(ctx, testGuildID, InitializationAction{Kind: "community.disable"}, actionRemote)
	require.NoError(t, err)
	_, err = manager.executeInitializationAction(ctx, testGuildID, InitializationAction{Kind: "community.enable"}, actionRemote)
	require.NoError(t, err)
	_, err = manager.executeInitializationAction(ctx, testGuildID, InitializationAction{
		Kind: "channel.update", ResourceID: rulesID, Spec: ChannelSpec{Key: "system.rules", Name: "规则", Kind: "text"},
	}, actionRemote)
	require.NoError(t, err)

	repositoryResource := insertDiscordResource(t, db, "forum.repository.record-test", "100000000000000052", "forum", "repo-record", "")
	_ = repositoryResource
	_, err = manager.executeInitializationAction(ctx, testGuildID, InitializationAction{
		Kind: "forum.repository.record", RepositoryID: seed.repositoryID.String(), Spec: ChannelSpec{Key: "forum.repository.record-test"},
	}, actionRemote)
	require.NoError(t, err)
	deleteResource := insertDiscordResource(t, db, "delete.test", "100000000000000053", "text", "delete", "")
	_ = deleteResource
	_, err = manager.executeInitializationAction(ctx, testGuildID, InitializationAction{
		Kind: "channel.delete", ResourceID: "100000000000000053",
	}, actionRemote)
	require.NoError(t, err)
	_, err = manager.executeInitializationAction(ctx, testGuildID, InitializationAction{Kind: "unknown"}, actionRemote)
	require.Error(t, err)
	require.True(t, actionRemote.disabled)
	require.True(t, actionRemote.enabled)
	require.True(t, actionRemote.updated)
	require.True(t, actionRemote.deleted)
	operationID, err := manager.CreateInitialization(ctx, seed.administratorID, InitializationPlan{
		Preflight: PreflightResult{GuildID: testGuildID, Mode: InitializationIncremental, Safe: true},
		Actions: []InitializationAction{{Kind: "channel.create", Spec: ChannelSpec{
			Key: "category.resume", Name: "Resume", Kind: "category",
		}}},
	}, "")
	require.NoError(t, err)
	paused, err := daemon.projectionsPaused(ctx, testGuildID)
	require.NoError(t, err)
	require.True(t, paused)
	require.NoError(t, daemon.resumeInitialization(ctx, testGuildID, actionRemote))
	paused, err = daemon.projectionsPaused(ctx, testGuildID)
	require.NoError(t, err)
	require.False(t, paused)
	resolved, err := manager.resolveChannelSpec(ctx, testGuildID, ChannelSpec{
		Key: "child", ParentKey: "category.codex.01", Name: "child", Kind: "text",
	})
	require.NoError(t, err)
	require.Equal(t, seed.codexCategoryID, resolved.ParentKey)
	_, err = manager.resolveChannelSpec(ctx, testGuildID, ChannelSpec{ParentKey: "missing", Kind: "text"})
	require.Error(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id, name) VALUES ('100000000000000888', 'empty')`)
	require.NoError(t, err)
	categoryKey, categoryID, err := manager.availableCodexCategory(ctx, "100000000000000888")
	require.NoError(t, err)
	require.Equal(t, "category.codex.01", categoryKey)
	require.Empty(t, categoryID)
	require.True(t, isRemoteStatus(&disgorest.Error{Response: &http.Response{StatusCode: http.StatusNotFound}}, http.StatusNotFound))
	operation, err := manager.Operation(ctx, operationID)
	require.NoError(t, err)
	require.Equal(t, "completed", operation.Status)
	require.NoError(t, daemon.resumeInitialization(ctx, testGuildID, actionRemote))

	exhaustedID, err := manager.CreateInitialization(ctx, seed.administratorID, InitializationPlan{
		Preflight: PreflightResult{GuildID: testGuildID, Mode: InitializationIncremental, Safe: true},
		Actions: []InitializationAction{
			{Kind: "unknown"},
			{Kind: "channel.create", Spec: ChannelSpec{Key: "after.exhausted", Name: "After exhausted", Kind: "text"}},
		},
	}, "")
	require.NoError(t, err)
	for range initializationMaxAttempts {
		require.Error(t, daemon.resumeInitialization(ctx, testGuildID, actionRemote))
	}
	require.NoError(t, daemon.resumeInitialization(ctx, testGuildID, actionRemote))
	var exhaustedStatus string
	var exhaustedAttempts int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT o.status, s.attempt_count
		FROM discord_initialization_operations o JOIN discord_initialization_steps s ON s.operation_id = o.id
		WHERE o.id = $1 AND s.ordinal = 1`, exhaustedID).Scan(&exhaustedStatus, &exhaustedAttempts))
	require.Equal(t, "failed", exhaustedStatus)
	require.Equal(t, initializationMaxAttempts, exhaustedAttempts)
	var pendingAfterExhausted int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_initialization_steps
		WHERE operation_id = $1 AND ordinal > 1 AND status = 'pending'`, exhaustedID).Scan(&pendingAfterExhausted))
	require.Equal(t, 1, pendingAfterExhausted)
	paused, err = daemon.projectionsPaused(ctx, testGuildID)
	require.NoError(t, err)
	require.False(t, paused)

	registerServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodPut, request.Method)
		require.Contains(t, request.URL.Path, "/applications/900/guilds/")
		var commands []struct {
			Name    string `json:"name"`
			Options []struct {
				Name    string `json:"name"`
				Options []struct {
					Name string `json:"name"`
				} `json:"options"`
			} `json:"options"`
		}
		require.NoError(t, json.NewDecoder(request.Body).Decode(&commands))
		var codexSubcommands, newOptions []string
		for _, command := range commands {
			if command.Name == "codex" {
				for _, option := range command.Options {
					codexSubcommands = append(codexSubcommands, option.Name)
					if option.Name == "new" {
						for _, child := range option.Options {
							newOptions = append(newOptions, child.Name)
						}
					}
				}
			}
		}
		require.Contains(t, codexSubcommands, "archive")
		require.Contains(t, codexSubcommands, "restore")
		require.Contains(t, codexSubcommands, "config")
		require.NotContains(t, codexSubcommands, "mode")
		require.Contains(t, newOptions, "mode")
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`[]`))
	}))
	t.Cleanup(registerServer.Close)
	registerRemote := NewDisgoRemote("token", registerServer.URL, registerServer.Client())
	t.Cleanup(func() { registerRemote.Close(context.Background()) })
	client := &bot.Client{ApplicationID: snowflake.ID(900), Rest: registerRemote.rest, Caches: cache.New()}
	self := discord.OAuth2User{User: discord.User{ID: snowflake.ID(900), Username: "bot", Bot: true}}
	client.Caches.SetSelfUser(self)
	require.NoError(t, (&DisgoConnector{guildID: testGuildID}).registerCommands(ctx, client))
	readyConnector := &DisgoConnector{manager: manager, guildID: testGuildID, logger: zap.NewNop(), client: client}
	readyConnector.onReady(&events.Ready{GenericEvent: events.NewGenericEvent(client, 8, 0),
		EventReady: gateway.EventReady{User: self}})
	readyConnector.onReady(&events.Ready{GenericEvent: events.NewGenericEvent(client, 8, 0),
		EventReady: gateway.EventReady{User: discord.OAuth2User{User: discord.User{ID: snowflake.ID(901)}}}})
	readyConnector.onResumed(&events.Resumed{GenericEvent: events.NewGenericEvent(client, 9, 0)})
	daemon.outboxInterval = time.Millisecond
	daemon.operationInterval = time.Millisecond
	daemon.projectionInterval = time.Millisecond
	daemon.permissionInterval = time.Millisecond
	backgroundCtx, stopBackground := context.WithTimeout(ctx, 25*time.Millisecond)
	defer stopBackground()
	require.ErrorIs(t, daemon.runBackground(backgroundCtx, testGuildID,
		&projectionRemote{guild: RemoteGuild{ID: testGuildID}}), context.DeadlineExceeded)
	openCtx, stopOpen := context.WithCancel(ctx)
	stopOpen()
	require.Error(t, NewDisgoConnector(manager, NewConversationService(db), &BindingService{store: store},
		testGuildID, "invalid-token", zap.NewNop()).Open(openCtx, nil))

	gatewayErr := errors.New("fake gateway stopped")
	daemon.newRemote = func(string, string) Remote { return &projectionRemote{guild: RemoteGuild{ID: testGuildID}} }
	daemon.newGateway = func(Settings, string) GatewayConnector { return serviceGateway{err: gatewayErr} }
	require.ErrorIs(t, daemon.Run(ctx), gatewayErr)

	require.NoError(t, manager.SaveSettings(ctx, SettingsInput{GuildID: testGuildID, Enabled: false}))
	runCtx, stopRun := context.WithCancel(ctx)
	stopRun()
	require.ErrorIs(t, daemon.Run(runCtx), context.Canceled)
}

func readySettingsForTest() Settings {
	return Settings{GuildID: testGuildID, Enabled: true, BotUserID: testBotID}
}

type serviceGateway struct{ err error }

func (g serviceGateway) Open(context.Context, *GatewaySession) error { return g.err }

type initializationActionRemote struct {
	disabled bool
	enabled  bool
	updated  bool
	deleted  bool
}

func (r *initializationActionRemote) Guild(context.Context, string) (RemoteGuild, error) {
	return RemoteGuild{}, nil
}
func (r *initializationActionRemote) DisableCommunity(context.Context, string) error {
	r.disabled = true
	return nil
}
func (r *initializationActionRemote) EnableCommunity(context.Context, string, string, string) error {
	r.enabled = true
	return nil
}
func (r *initializationActionRemote) CreateChannel(context.Context, string, ChannelSpec, string) (RemoteChannel, error) {
	return RemoteChannel{ID: "100000000000000060"}, nil
}
func (r *initializationActionRemote) UpdateChannel(context.Context, string, ChannelSpec) error {
	r.updated = true
	return nil
}
func (r *initializationActionRemote) DeleteChannel(context.Context, string) error {
	r.deleted = true
	return nil
}
func (r *initializationActionRemote) Send(context.Context, OutboxItem) (json.RawMessage, error) {
	return nil, nil
}
func (r *initializationActionRemote) Close(context.Context) {}

type projectionRemote struct{ guild RemoteGuild }

func (r *projectionRemote) Guild(context.Context, string) (RemoteGuild, error) { return r.guild, nil }
func (r *projectionRemote) DisableCommunity(context.Context, string) error     { return nil }
func (r *projectionRemote) EnableCommunity(context.Context, string, string, string) error {
	return nil
}
func (r *projectionRemote) CreateChannel(context.Context, string, ChannelSpec, string) (RemoteChannel, error) {
	return RemoteChannel{}, nil
}
func (r *projectionRemote) UpdateChannel(context.Context, string, ChannelSpec) error { return nil }
func (r *projectionRemote) DeleteChannel(context.Context, string) error              { return nil }
func (r *projectionRemote) Send(context.Context, OutboxItem) (json.RawMessage, error) {
	return nil, nil
}
func (r *projectionRemote) Close(context.Context) {}

func bindDiscordConversationSessionForTest(t *testing.T, db *sql.DB,
	conversationID uuid.UUID,
) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var sessionID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_sessions(
		workspace_id,workspace_project_id,agent_profile_id,title,lifecycle_state,
		model,reasoning_effort,service_tier,collaboration_mode,settings_version,last_activity_at)
		SELECT forum.workspace_id,conversation.workspace_project_id,
			conversation.agent_profile_id,COALESCE(conversation.generated_title,conversation.title),
			conversation.lifecycle_state,conversation.model,conversation.reasoning_effort,
			COALESCE(conversation.service_tier,'standard'),conversation.collaboration_mode,
			conversation.settings_revision,conversation.last_activity_at
		FROM discord_conversations conversation
		JOIN discord_forums forum ON forum.id=conversation.forum_id
		WHERE conversation.id=$1 RETURNING id`, conversationID).Scan(&sessionID))
	_, err := db.ExecContext(ctx, `UPDATE discord_conversations SET session_id=$2
		WHERE id=$1`, conversationID, sessionID)
	require.NoError(t, err)
	return sessionID
}

func discordDatabase(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "postgres:18.3-bookworm@sha256:80630f83606d8db77d30b3851b16a9f78be2d0d4dda6f7b82a1fdca5ebe3acba",
			Env: map[string]string{
				"POSTGRES_DB": "tyrs_hand", "POSTGRES_USER": "tyrs_hand", "POSTGRES_PASSWORD": "test-password",
			},
			ExposedPorts: []string{"5432/tcp"},
			WaitingFor: wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testcontainers.TerminateContainer(container)) })
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	for attempt := 0; err != nil && attempt < 50; attempt++ {
		time.Sleep(100 * time.Millisecond)
		port, err = container.MappedPort(ctx, "5432/tcp")
	}
	require.NoError(t, err)
	db, err := database.Open(ctx, "postgres://tyrs_hand:test-password@"+host+":"+port.Port()+"/tyrs_hand?sslmode=disable")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}
