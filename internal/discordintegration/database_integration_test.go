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

	remoteGuild := RemoteGuild{ID: testGuildID, CommunityEnabled: true, Channels: []RemoteChannel{
		{ID: seed.codexCategoryID, Name: "Codex 会话 01", Kind: "category"},
	}}
	personalPlan, err := manager.PersonalForumPlan(ctx, remoteGuild, "1003")
	require.NoError(t, err)
	require.True(t, personalPlan.Preflight.Safe)
	require.Contains(t, personalPlan.Actions[len(personalPlan.Actions)-1].Kind, "forum.personal.record")
	serverPlan, err := manager.ServerInitializationPlan(ctx, remoteGuild, InitializationIncremental)
	require.NoError(t, err)
	require.True(t, serverPlan.Preflight.Safe)
	require.NotEmpty(t, serverPlan.Actions)

	require.Error(t, manager.SetForumAccess(ctx, seed.personalForumID, "1002", "admin", seed.administratorID))
	require.NoError(t, manager.SetForumAccess(ctx, seed.personalForumID, "1002", AccessReadOnly, seed.administratorID))
	require.NoError(t, manager.SetForumAccess(ctx, seed.personalForumID, "1003", AccessOperator, seed.administratorID))
	require.NoError(t, manager.DeleteForumAccess(ctx, seed.personalForumID, "1002"))
	var permissionPayload []byte
	require.NoError(t, db.QueryRowContext(ctx, `SELECT payload FROM integration_outbox
		WHERE operation_key = $1`, "forum-permissions:"+seed.personalForumID.String()).Scan(&permissionPayload))
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
	completeOutboxForTest(t, ctx, db, "task-card:"+seed.workItemID.String(), nil)
	completeOutboxForTest(t, ctx, db, "task-archive:"+seed.workItemID.String(), nil)
	require.NoError(t, daemon.refreshTodoProjections(ctx, testGuildID))

	var taskType, todoType string
	var taskPayload, todoPayload, statusPayload, alertsPayload []byte
	require.NoError(t, db.QueryRowContext(ctx, `SELECT operation_type FROM integration_outbox
		WHERE operation_key = $1`, "task-post:"+seed.workItemID.String()).Scan(&taskType))
	require.Equal(t, "forum.post.create", taskType)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT payload FROM integration_outbox
		WHERE operation_key = $1`, "task-post:"+seed.workItemID.String()).Scan(&taskPayload))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT operation_type FROM integration_outbox
		WHERE operation_key = 'projection:todo:1001'`).Scan(&todoType))
	require.Equal(t, "forum.post.create", todoType)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT payload FROM integration_outbox
		WHERE operation_key = 'projection:todo:1001'`).Scan(&todoPayload))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT payload FROM integration_outbox
		WHERE operation_key = 'projection:system.status'`).Scan(&statusPayload))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT payload FROM integration_outbox
		WHERE operation_key = 'projection:system.alerts'`).Scan(&alertsPayload))
	for _, payload := range [][]byte{taskPayload, todoPayload, statusPayload, alertsPayload} {
		require.Contains(t, string(payload), `"embeds"`)
		require.Contains(t, string(payload), `"content": ""`)
	}
	intentID := insertProjectionIntent(t, db, seed.workItemID, seed.repositoryID,
		"projection-job-retry", "alice")
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_intents SET status = 'completed',
		finished_at = now(), created_at = now() + interval '1 second' WHERE id = $1`, intentID)
	require.NoError(t, err)
	todoCard, err := daemon.todoCard(ctx, testGuildID, "1001")
	require.NoError(t, err)
	require.Equal(t, "✅ 待我处理", todoCard.Title)
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

const (
	testGuildID = "100000000000000001"
	testBotID   = "100000000000000099"
)

type discordManagerSeed struct {
	administratorID          uuid.UUID
	personalForumID          uuid.UUID
	workItemID               uuid.UUID
	codexCategoryID          string
	repositoryForumChannelID string
	personalForumChannelID   string
	repositoryID             uuid.UUID
	repositoryForumID        uuid.UUID
}

func seedDiscordManagerData(t *testing.T, db *sql.DB) discordManagerSeed {
	t.Helper()
	ctx := context.Background()
	seed := discordManagerSeed{
		codexCategoryID: "100000000000000011", personalForumChannelID: "100000000000000012",
		repositoryForumChannelID: "100000000000000022",
	}
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO administrators
		(username, password_hash, totp_secret_ciphertext) VALUES ('discord-admin', 'hash', $1) RETURNING id`,
		[]byte("secret")).Scan(&seed.administratorID))
	for _, user := range []struct{ id, login string }{{"1001", "alice"}, {"1002", "bob"}, {"1003", "charlie"}} {
		_, err := db.ExecContext(ctx, `INSERT INTO discord_members
			(guild_id, discord_user_id, username, display_name) VALUES ($1, $2, $3, $3)`, testGuildID, user.id, user.login)
		require.NoError(t, err)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO discord_identity_bindings
		(guild_id, discord_user_id, github_user_id, github_login) VALUES ($1, '1001', 101, 'alice')`, testGuildID)
	require.NoError(t, err)

	categoryResource := insertDiscordResource(t, db, "category.codex.01", seed.codexCategoryID, "category", "Codex 会话 01", "")
	_ = categoryResource
	personalResource := insertDiscordResource(t, db, "forum.personal.1001", seed.personalForumChannelID, "forum", "codex-alice", seed.codexCategoryID)
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_forums
		(guild_id, resource_id, forum_type, owner_discord_user_id) VALUES ($1, $2, 'personal', '1001') RETURNING id`,
		testGuildID, personalResource).Scan(&seed.personalForumID))

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
		VALUES ($1, $2, 'repository', $3) RETURNING id`, testGuildID, repositoryResource, repositoryID).Scan(&seed.repositoryForumID))
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
			RepositoryID: repositoryID, AgentProfileID: profileID, ContextVersion: 1,
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
	require.NoError(t, db.QueryRowContext(ctx, `UPDATE integration_outbox SET status = 'sending',
		lease_token = $2, lease_expires_at = now() + interval '1 minute'
		WHERE operation_key = $1 RETURNING id, operation_key, operation_type, route_key, payload,
		COALESCE(nonce, ''), attempt_count, max_attempts`, key, item.LeaseToken).
		Scan(&id, &item.OperationKey, &item.OperationType, &item.RouteKey, &item.Payload,
			&item.Nonce, &item.Attempt, &item.MaxAttempts))
	item.ID = id.String()
	require.NoError(t, NewSQLoutbox(db).Complete(ctx, item, response))
}

func testGatewayHandlers(t *testing.T, ctx context.Context, db *sql.DB, manager *Manager, seed discordManagerSeed) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/channels/"):
			threadID := strings.TrimPrefix(request.URL.Path, "/channels/")
			if threadID == "2099" {
				_, _ = response.Write([]byte(fmt.Sprintf(`{"id":%q,"guild_id":%q,"parent_id":"2999","type":0,"name":"general","position":0,"permission_overwrites":[],"rate_limit_per_user":0,"nsfw":false}`, threadID, testGuildID)))
				return
			}
			_, _ = response.Write([]byte(fmt.Sprintf(`{"id":%q,"guild_id":%q,"parent_id":%q,"type":11,"name":"Conversation","owner_id":"1001","message_count":1,"member_count":1,"rate_limit_per_user":0,"thread_metadata":{"archived":false,"auto_archive_duration":10080,"archive_timestamp":"2026-07-18T00:00:00Z","locked":false}}`,
				threadID, testGuildID, seed.personalForumChannelID)))
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

	blank := newComponentEvent(t, client, "5001", "2001", "conversation-blank:"+conversationID.String(), nil)
	connector.onComponent(blank)
	var conversationStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM discord_conversations WHERE id = $1`, conversationID).Scan(&conversationStatus))
	require.Equal(t, "active", conversationStatus)

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

	messageEvent = newMessageEvent(t, client, "2002", "3002", "repository message")
	connector.onMessage(messageEvent)
	var repositoryConversationID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM discord_conversations
		WHERE guild_id = $1 AND thread_id = '2002'`, testGuildID).Scan(&repositoryConversationID))
	var repositoryForumID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM discord_forums
		WHERE repository_id = $1`, seed.repositoryID).Scan(&repositoryForumID))
	_, err = db.ExecContext(ctx, `INSERT INTO discord_forum_access(forum_id, discord_user_id, access_level)
		VALUES ($1, '1001', 'readonly')`, repositoryForumID)
	require.NoError(t, err)
	connector.onComponent(newComponentEvent(t, client, "5003", "2002",
		"conversation-repository:"+repositoryConversationID.String(), nil))
	connector.onComponent(newComponentEvent(t, client, "5004", "2002",
		"conversation-repository-select:"+repositoryConversationID.String(), []string{seed.repositoryID.String()}))
	var selectedRepository uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT repository_id FROM discord_conversations
		WHERE id = $1`, repositoryConversationID).Scan(&selectedRepository))
	require.Equal(t, seed.repositoryID, selectedRepository)

	connector.onComponent(newComponentEvent(t, client, "5005", "2001", "github-unbind-confirm:1001", nil))
	var activeBinding int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_identity_bindings
		WHERE guild_id = $1 AND discord_user_id = '1001' AND status = 'active'`, testGuildID).Scan(&activeBinding))
	require.Zero(t, activeBinding)
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
	daemon := NewDaemon(manager, NewConversationService(db), &BindingService{store: store}, appManager, nil, zap.NewNop())
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

	personalResource := insertDiscordResource(t, db, "forum.personal.1002", "100000000000000051", "forum", "codex-bob", seed.codexCategoryID)
	_ = personalResource
	_, err = manager.executeInitializationAction(ctx, testGuildID, InitializationAction{
		Kind: "forum.personal.record", OwnerUserID: "1002", Spec: ChannelSpec{Key: "forum.personal.1002"},
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
	require.NoError(t, err)
	db, err := database.Open(ctx, "postgres://tyrs_hand:test-password@"+host+":"+port.Port()+"/tyrs_hand?sslmode=disable")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}
