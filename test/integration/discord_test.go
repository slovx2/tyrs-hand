//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/workerregistry"
	"github.com/stretchr/testify/require"
)

func TestDiscordPersistencePermissionsAndRecovery(t *testing.T) {
	db := postgresDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	manager := discordintegration.NewManager(db, nil)
	service := discordintegration.NewConversationService(db)
	seed := seedDiscord(t, db)

	first := discordintegration.IncomingMessage{
		GuildID: seed.guildID, ForumID: seed.forumChannelID, ThreadID: "2001", MessageID: "3001",
		DiscordUserID: "1001", DisplayName: "Owner", Username: "owner", Title: "first", Body: "hello",
		Attachments: []discordintegration.IncomingAttachment{{
			ID: "4001", URL: "https://cdn.discordapp.com/attachments/1/2/file.txt",
			Filename: "file.txt", MediaType: "text/plain", Size: 4,
		}},
	}
	conversationID, err := service.BeginPost(ctx, first)
	require.NoError(t, err)
	duplicateID, err := service.BeginPost(ctx, first)
	require.NoError(t, err)
	require.Equal(t, conversationID, duplicateID)

	readonly := first
	readonly.MessageID, readonly.DiscordUserID, readonly.DisplayName = "3002", "1002", "Read Only"
	readonly.Body, readonly.Attachments = "cannot write", nil
	require.ErrorIs(t, service.Reply(ctx, readonly), discordintegration.ErrReadOnly)

	operator := readonly
	operator.MessageID, operator.DiscordUserID, operator.DisplayName = "3003", "1003", "Operator"
	operator.Body = "steer this"
	require.NoError(t, service.Reply(ctx, operator))
	require.NoError(t, service.Reply(ctx, operator))
	memberStore := discordintegration.NewSQLoutbox(db)
	memberOperation, err := memberStore.Claim(ctx, 30*time.Second)
	require.NoError(t, err)
	require.NotNil(t, memberOperation)
	require.Equal(t, "thread.member.add", memberOperation.OperationType)
	completeDiscordDelivery(t, ctx, memberStore, memberOperation, nil)
	var jobs, messages, attachments int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_intents
		WHERE discord_conversation_id = $1`, conversationID).Scan(&jobs))
	require.Zero(t, jobs)
	require.NoError(t, service.FinalizeConfiguration(ctx, conversationID, "1001"))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_intents
		WHERE discord_conversation_id = $1`, conversationID).Scan(&jobs))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_input_messages
		WHERE conversation_id = $1`, conversationID).Scan(&messages))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_attachments
		WHERE message_id = '3001'`).Scan(&attachments))
	require.Equal(t, 2, jobs)
	require.Equal(t, 2, messages)
	require.Equal(t, 1, attachments)

	_, err = service.Stop(ctx, seed.guildID, first.ThreadID, "1002")
	require.ErrorIs(t, err, discordintegration.ErrReadOnly)
	stopped, err := service.Stop(ctx, seed.guildID, first.ThreadID, "1003")
	require.NoError(t, err)
	require.EqualValues(t, 2, stopped)
	var canceledMessages int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_input_messages
		WHERE conversation_id = $1 AND status = 'canceled'`, conversationID).Scan(&canceledMessages))
	require.Equal(t, 2, canceledMessages)

	bindings := discordintegration.NewSQLBindingStore(db)
	require.NoError(t, bindings.Unbind(ctx, seed.guildID, "1003"))
	rebound, err := bindings.Bind(ctx, discordintegration.Binding{
		GuildID: seed.guildID, DiscordUserID: "1003", GitHubUserID: 303, GitHubLogin: "operator-new",
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, rebound.Version)
	var snapshotLogin string
	var snapshotVersion int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT github_login, binding_version
		FROM discord_input_messages WHERE message_id = '3003'`).Scan(&snapshotLogin, &snapshotVersion))
	require.Equal(t, "operator-old", snapshotLogin)
	require.EqualValues(t, 1, snapshotVersion)

	testGatewayPersistence(t, ctx, manager, seed.guildID)
	_, err = db.ExecContext(ctx, `UPDATE integration_outbox SET status = 'completed',
		available_at = now(), updated_at = now() WHERE integration = 'discord'
		AND status IN ('pending','retrying')`)
	require.NoError(t, err)
	testOutboxRecovery(t, ctx, db, seed.guildID, conversationID)
	testInitializationRecovery(t, ctx, db, manager, seed)
}

type captureGateway struct {
	session *discordintegration.GatewaySession
}

func (c *captureGateway) Open(_ context.Context, session *discordintegration.GatewaySession) error {
	c.session = session
	return nil
}

func testGatewayPersistence(t *testing.T, ctx context.Context, manager *discordintegration.Manager, guildID string) {
	t.Helper()
	session := discordintegration.GatewaySession{
		GuildID: guildID, SessionID: "session-1", ResumeURL: "wss://resume.example", Sequence: 42,
	}
	require.NoError(t, manager.SaveGatewaySession(ctx, session))
	connector := &captureGateway{}
	require.NoError(t, discordintegration.NewGatewayRunner(manager, guildID, connector).Run(ctx))
	require.NotNil(t, connector.session)
	require.Equal(t, session, *connector.session)

	inserted, err := manager.RecordInboundEvent(ctx, "message:3001", guildID, "MESSAGE_CREATE", map[string]string{"id": "3001"})
	require.NoError(t, err)
	require.True(t, inserted)
	inserted, err = manager.RecordInboundEvent(ctx, "message:3001", guildID, "MESSAGE_CREATE", map[string]string{"id": "3001"})
	require.NoError(t, err)
	require.False(t, inserted)
	require.NoError(t, manager.CompleteInboundEvent(ctx, "message:3001", errors.New("failed once")))
}

func testOutboxRecovery(t *testing.T, ctx context.Context, db *sql.DB, guildID string, conversationID uuid.UUID) {
	t.Helper()
	store := discordintegration.NewSQLoutbox(db)
	projectionKey := "conversation:" + conversationID.String() + ":message:3001"
	require.NoError(t, discordintegration.ProjectConversationStatus(ctx, db, guildID, "2001", conversationID,
		"3001", uuid.Nil, discordintegration.ConversationRunning, "processing"))
	_, err := db.ExecContext(ctx, `UPDATE integration_outbox SET available_at = now()
		WHERE operation_key = $1`, "projection:"+projectionKey)
	require.NoError(t, err)
	first, err := store.Claim(ctx, 30*time.Second)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.Equal(t, "message.create", first.OperationType)
	require.Contains(t, string(first.Payload), `"card"`)
	require.NotContains(t, string(first.Payload), `"embeds"`)
	require.Contains(t, string(first.Payload), "思考中")

	require.NoError(t, discordintegration.ProjectConversationStatus(ctx, db, guildID, "2001", conversationID,
		"3001", uuid.Nil, discordintegration.ConversationCompleted, "completed"))
	response := json.RawMessage(`{"messageId":"5001"}`)
	completeDiscordDelivery(t, ctx, store, first, response)
	var status string
	var applied, desired int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT o.status, p.applied_version, p.desired_version
		FROM integration_outbox o JOIN discord_projections p ON p.projection_key = $1
		WHERE o.operation_key = 'projection:' || $1`, projectionKey).Scan(&status, &applied, &desired))
	require.Equal(t, "pending", status)
	require.Zero(t, applied)
	require.Greater(t, desired, applied)

	_, err = db.ExecContext(ctx, `UPDATE integration_outbox SET available_at = now()
		WHERE operation_key = 'projection:' || $1`, projectionKey)
	require.NoError(t, err)
	latest, err := store.Claim(ctx, 30*time.Second)
	require.NoError(t, err)
	require.NotNil(t, latest)
	require.Equal(t, "message.update", latest.OperationType)
	require.Contains(t, string(latest.Payload), "已完成")
	completeDiscordDelivery(t, ctx, store, latest, nil)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT applied_version, desired_version
		FROM discord_projections WHERE projection_key = $1`, projectionKey).Scan(&applied, &desired))
	require.Equal(t, desired, applied)

	require.NoError(t, discordintegration.ProjectConversationStatus(ctx, db, guildID, "2001", conversationID,
		"3002", uuid.Nil, discordintegration.ConversationRunning, "next turn"))
	nextTurn, err := store.Claim(ctx, 30*time.Second)
	require.NoError(t, err)
	require.NotNil(t, nextTurn)
	require.Equal(t, "message.create", nextTurn.OperationType)
	require.NotEqual(t, first.OperationKey, nextTurn.OperationKey)
	completeDiscordDelivery(t, ctx, store, nextTurn, json.RawMessage(`{"messageId":"5002"}`))

	require.NoError(t, discordintegration.ProjectConversationReply(ctx, db, "2001",
		conversationID, "3002", uuid.Nil, "final reply", "agentMessage"))
	reply, err := store.Claim(ctx, 30*time.Second)
	require.NoError(t, err)
	require.NotNil(t, reply)
	require.Equal(t, "message.create", reply.OperationType)
	require.JSONEq(t, `{"channelId":"2001","content":"final reply"}`, string(reply.Payload))
	completeDiscordDelivery(t, ctx, store, reply, json.RawMessage(`{"messageId":"5003"}`))

	require.NoError(t, discordintegration.ProjectConversationReply(ctx, db, "2001",
		conversationID, "3003", uuid.Nil, "operator reply", "agentMessage"))
	mentionedReply, err := store.Claim(ctx, 30*time.Second)
	require.NoError(t, err)
	require.NotNil(t, mentionedReply)
	require.JSONEq(t, `{
		"channelId":"2001",
		"content":"<@1003> operator reply",
		"mentionUserIds":["1003"]
	}`, string(mentionedReply.Payload))
	completeDiscordDelivery(t, ctx, store, mentionedReply, json.RawMessage(`{"messageId":"5004"}`))
	testLongConversationReply(t, ctx, store, db, guildID, conversationID)

	require.NoError(t, store.Enqueue(ctx, "crash-recovery", "message.create", "channels/2001/messages",
		map[string]string{"channelId": "2001", "content": "recover"}, "crash-nonce"))
	crashed, err := store.Claim(ctx, time.Second)
	require.NoError(t, err)
	require.NotNil(t, crashed)
	_, err = db.ExecContext(ctx, `UPDATE integration_outbox SET lease_expires_at = now() - interval '1 second'
		WHERE id = $1`, crashed.ID)
	require.NoError(t, err)
	recovered, err := store.Claim(ctx, time.Second)
	require.NoError(t, err)
	require.Nil(t, recovered, "结果未知的外部写入不得自动重发")
	var ambiguousStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM integration_outbox WHERE id=$1`,
		crashed.ID).Scan(&ambiguousStatus))
	require.Equal(t, "ambiguous", ambiguousStatus)
	resolved, err := store.ResolveAmbiguousDelivery(ctx, crashed.OperationKey,
		json.RawMessage(`{"messageId":"recovered-message"}`))
	require.NoError(t, err)
	require.NoError(t, store.Apply(ctx, *resolved))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM integration_outbox WHERE id=$1`,
		crashed.ID).Scan(&ambiguousStatus))
	require.Equal(t, "completed", ambiguousStatus)

	require.NoError(t, store.Enqueue(ctx, "known-retry", "message.create", "channels/2001/messages",
		map[string]string{"channelId": "2001", "content": "retry"}, "retry-nonce"))
	recovered, err = store.Claim(ctx, time.Second)
	require.NoError(t, err)
	require.NotNil(t, recovered)
	require.NoError(t, store.RetryDelivery(ctx, *recovered, time.Now().Add(-time.Second),
		errors.New("retry once")))
	retried, err := store.Claim(ctx, time.Second)
	require.NoError(t, err)
	require.NotNil(t, retried)
	require.NoError(t, store.FailDelivery(ctx, *retried, errors.New("permanent failure")))
	var failedStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM integration_outbox WHERE id = $1`, retried.ID).Scan(&failedStatus))
	require.Equal(t, "failed", failedStatus)
}

func testLongConversationReply(t *testing.T, ctx context.Context,
	store *discordintegration.SQLoutbox, db *sql.DB, guildID string, conversationID uuid.UUID,
) {
	t.Helper()
	line := strings.Repeat("完整内容", 24)
	answer := strings.Repeat(line+"\n", 80)
	require.NoError(t, discordintegration.ProjectConversationReply(ctx, db, "2001",
		conversationID, "3001", uuid.Nil, answer, "agentMessage"))
	prefix := "projection:conversation-reply:" + conversationID.String() + ":message:3001"
	var createdContents, updatedContents, createdIDs []string
	for step := 0; step < 30; step++ {
		item, err := store.Claim(ctx, 30*time.Second)
		require.NoError(t, err)
		if item == nil {
			break
		}
		require.True(t, strings.HasPrefix(item.OperationKey, prefix), item.OperationKey)
		var payload struct {
			Content string `json:"content"`
		}
		require.NoError(t, json.Unmarshal(item.Payload, &payload))
		require.LessOrEqual(t, len([]rune(payload.Content)), 1900)
		switch item.OperationType {
		case "message.create":
			messageID := fmt.Sprintf("60%02d", len(createdIDs)+1)
			createdIDs = append(createdIDs, messageID)
			createdContents = append(createdContents, payload.Content)
			completeDiscordDelivery(t, ctx, store, item,
				json.RawMessage(fmt.Sprintf(`{"messageId":%q}`, messageID)))
		case "message.update":
			updatedContents = append(updatedContents, payload.Content)
			completeDiscordDelivery(t, ctx, store, item, json.RawMessage(`{}`))
		default:
			t.Fatalf("unexpected reply operation %s", item.OperationType)
		}
	}
	require.GreaterOrEqual(t, len(createdContents), 3)
	require.Empty(t, updatedContents)
	require.Contains(t, createdContents[0], "<@1001>")
	for index, content := range createdContents {
		require.Contains(t, content,
			fmt.Sprintf("**Codex 回复 · %d/%d**", index+1, len(createdContents)))
		if index > 0 {
			require.NotContains(t, content, "<@1001>")
		}
		require.NotContains(t, content, "【上接消息】")
		require.NotContains(t, content, "【内容未完整，下接消息】")
	}
}

func completeDiscordDelivery(t *testing.T, ctx context.Context,
	store *discordintegration.SQLoutbox, item *discordintegration.OutboxItem,
	response json.RawMessage,
) {
	t.Helper()
	require.NoError(t, store.RecordDelivery(ctx, item, response))
	require.NoError(t, store.Apply(ctx, *item))
}

type ambiguousInitializationRemote struct {
	mu       sync.Mutex
	guild    discordintegration.RemoteGuild
	creates  int
	failOnce bool
}

func (r *ambiguousInitializationRemote) Guild(context.Context, string) (discordintegration.RemoteGuild, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := r.guild
	result.Channels = append([]discordintegration.RemoteChannel(nil), r.guild.Channels...)
	return result, nil
}
func (r *ambiguousInitializationRemote) DisableCommunity(context.Context, string) error { return nil }
func (r *ambiguousInitializationRemote) EnableCommunity(context.Context, string, string, string) error {
	return nil
}
func (r *ambiguousInitializationRemote) CreateChannel(_ context.Context, _ string, spec discordintegration.ChannelSpec, marker string) (discordintegration.RemoteChannel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.creates++
	channel := discordintegration.RemoteChannel{ID: "9001", Name: spec.Name, Kind: spec.Kind}
	if spec.Kind != "category" {
		channel.Topic = marker
	}
	r.guild.Channels = append(r.guild.Channels, channel)
	if r.failOnce {
		r.failOnce = false
		return discordintegration.RemoteChannel{}, discordintegration.ErrAmbiguousWrite
	}
	return channel, nil
}
func (r *ambiguousInitializationRemote) UpdateChannel(context.Context, string, discordintegration.ChannelSpec) error {
	return nil
}
func (r *ambiguousInitializationRemote) DeleteChannel(context.Context, string) error { return nil }
func (r *ambiguousInitializationRemote) Send(context.Context, discordintegration.OutboxItem) (json.RawMessage, error) {
	return nil, nil
}
func (r *ambiguousInitializationRemote) Close(context.Context) {}

func testInitializationRecovery(t *testing.T, ctx context.Context, db *sql.DB, manager *discordintegration.Manager, seed discordSeed) {
	t.Helper()
	remote := &ambiguousInitializationRemote{guild: discordintegration.RemoteGuild{ID: seed.guildID}, failOnce: true}
	plan := discordintegration.InitializationPlan{
		Preflight: discordintegration.PreflightResult{GuildID: seed.guildID, Mode: discordintegration.InitializationIncremental, Safe: true},
		Actions: []discordintegration.InitializationAction{{
			Kind: "channel.create", Spec: discordintegration.ChannelSpec{Key: "category.recovery", Name: "恢复测试", Kind: "category"},
		}},
	}
	operationID, err := manager.CreateInitialization(ctx, seed.administrator, plan, "")
	require.NoError(t, err)
	require.ErrorIs(t, manager.RunInitialization(ctx, operationID, remote), discordintegration.ErrAmbiguousWrite)
	require.NoError(t, manager.RunInitialization(ctx, operationID, remote))
	operation, err := manager.Operation(ctx, operationID)
	require.NoError(t, err)
	require.Equal(t, "completed", operation.Status)
	require.Equal(t, 1, remote.creates, "模糊成功后必须先对账，不能重复创建 Category")
	var resources int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_resources
		WHERE guild_id = $1 AND resource_key = 'category.recovery'`, seed.guildID).Scan(&resources))
	require.Equal(t, 1, resources)
}

type discordSeed struct {
	guildID        string
	forumChannelID string
	administrator  uuid.UUID
}

func seedDiscord(t *testing.T, db *sql.DB) discordSeed {
	t.Helper()
	ctx := context.Background()
	seed := discordSeed{guildID: "100000000000000001", forumChannelID: "100000000000000010"}
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO administrators
		(username, password_hash, totp_secret_ciphertext) VALUES ('discord-admin', 'hash', $1) RETURNING id`,
		[]byte("secret")).Scan(&seed.administrator))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_guilds
		(guild_id, name, enabled, community_enabled, bot_user_id)
		VALUES ($1, 'test', true, true, '100000000000000099') RETURNING guild_id`, seed.guildID).Scan(&seed.guildID))
	nodes := workerregistry.NewService(db)
	node, _, err := nodes.Create(ctx, "discord-test", []string{"discord"}, 2)
	require.NoError(t, err)
	require.NoError(t, nodes.SetDefaults(ctx, workerregistry.Defaults{DiscordWorkerID: &node.ID}))
	var workspaceID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO worker_workspaces
		(guild_id, owner_discord_user_id, worker_id)
		VALUES ($1, '1001', $2) RETURNING id`, seed.guildID, node.ID).
		Scan(&workspaceID))
	var projectID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_projects
		(workspace_id, relative_path, name, project_kind, availability_status)
		VALUES ($1, 'workspaces/atlas', 'atlas', 'git', 'available') RETURNING id`,
		workspaceID).Scan(&projectID))
	var resourceID, forumID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_resources
		(guild_id, resource_key, discord_id, kind, name, managed_marker)
		VALUES ($1, 'forum.workspace.owner', $2, 'forum', 'codex-owner', '[tyrs-hand:forum.workspace.owner]') RETURNING id`,
		seed.guildID, seed.forumChannelID).Scan(&resourceID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_forums
		(guild_id, resource_id, forum_type, owner_discord_user_id,
		 workspace_project_id, workspace_id)
		VALUES ($1, $2, 'workspace', '1001', $3, $4) RETURNING id`, seed.guildID, resourceID,
		projectID, workspaceID).Scan(&forumID))
	for _, userID := range []string{"1001", "1002", "1003"} {
		_, err := db.ExecContext(ctx, `INSERT INTO discord_members
			(guild_id, discord_user_id, username, display_name) VALUES ($1, $2, $2, $2)`, seed.guildID, userID)
		require.NoError(t, err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO discord_forum_access(forum_id, discord_user_id, access_level, granted_by)
		VALUES ($1, '1002', 'readonly', $2), ($1, '1003', 'operator', $2)`, forumID, seed.administrator)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_identity_bindings
		(guild_id, discord_user_id, github_user_id, github_login)
		VALUES ($1, '1001', 101, 'owner'), ($1, '1003', 103, 'operator-old')`, seed.guildID)
	require.NoError(t, err)
	return seed
}
