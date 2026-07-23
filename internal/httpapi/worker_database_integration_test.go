//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/executionnode"
	"github.com/slovx2/tyrs-hand/internal/participantidentity"
	"github.com/slovx2/tyrs-hand/internal/secrets"
	"github.com/slovx2/tyrs-hand/internal/security"
	platformsettings "github.com/slovx2/tyrs-hand/internal/settings"
	"github.com/slovx2/tyrs-hand/internal/sshconfig"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

func TestWorkerAPIPlacementLeaseEventsAndIdempotency(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	nodes := server.nodes
	nodeA, enrollmentA, err := nodes.Create(ctx, "home-a", []string{"github", "discord"}, 2)
	require.NoError(t, err)
	nodeB, enrollmentB, err := nodes.Create(ctx, "home-b", []string{"github", "discord"}, 2)
	require.NoError(t, err)
	_, enrollmentGitHubOnly, err := nodes.Create(ctx, "github-only", []string{"github"}, 1)
	require.NoError(t, err)

	clientA := workerprotocol.NewClient(endpoint, "", 5*time.Second)
	enrolledA, err := clientA.Enroll(ctx, enrollmentA)
	require.NoError(t, err)
	clientA.SetCredential(enrolledA.Credential)
	_, err = clientA.Enroll(ctx, enrollmentA)
	require.Error(t, err, "Enrollment Token 只能消费一次")
	rotationToken, err := nodes.NewEnrollment(ctx, nodeA.ID)
	require.NoError(t, err)
	rotated, err := workerprotocol.NewClient(endpoint, "", 5*time.Second).Enroll(ctx, rotationToken)
	require.NoError(t, err)
	require.Error(t, clientA.Heartbeat(ctx, workerprotocol.HeartbeatRequest{
		WorkerVersion: "old", ProtocolVersion: workerprotocol.Version,
	}), "凭据轮换后旧节点 Token 必须立即失效")
	clientA.SetCredential(rotated.Credential)
	require.NoError(t, clientA.Heartbeat(ctx, workerprotocol.HeartbeatRequest{
		WorkerVersion: "test", ProtocolVersion: workerprotocol.Version,
	}))
	require.NoError(t, clientA.Heartbeat(ctx, workerprotocol.HeartbeatRequest{
		WorkerVersion: "future", ProtocolVersion: workerprotocol.Version + 1,
	}), "协议不兼容时仍允许心跳上报")
	_, err = clientA.Claim(ctx, workerprotocol.ClaimRequest{WorkerID: "worker-a", Role: "github"})
	require.Error(t, err, "协议不兼容时必须拒绝 Claim")
	require.NoError(t, clientA.Heartbeat(ctx, workerprotocol.HeartbeatRequest{
		WorkerVersion: "test", ProtocolVersion: workerprotocol.Version,
	}))
	_, credentialB, err := nodes.Enroll(ctx, enrollmentB)
	require.NoError(t, err)
	clientB := workerprotocol.NewClient(endpoint, credentialB, 5*time.Second)
	_, githubOnlyCredential, err := nodes.Enroll(ctx, enrollmentGitHubOnly)
	require.NoError(t, err)
	githubOnlyClient := workerprotocol.NewClient(endpoint, githubOnlyCredential, 5*time.Second)
	_, err = githubOnlyClient.Claim(ctx, workerprotocol.ClaimRequest{
		WorkerID: "github-only", Role: "discord",
	})
	require.Error(t, err, "节点不能越权领取未授权角色")
	require.NoError(t, nodes.SetDefaults(ctx, executionnode.Defaults{
		GitHubNodeID: &nodeA.ID, DiscordNodeID: &nodeA.ID,
	}))

	repositoryID, firstItemID, profileID := seedWorkerGitHubQueue(t, db, 1)
	firstIntent := enqueueWorkerIntent(t, db, repositoryID, firstItemID, profileID, "first")
	assertPlacement(t, db, firstItemID, firstIntent, nodeA.ID, "queued")

	require.NoError(t, nodes.SetDefaults(ctx, executionnode.Defaults{
		GitHubNodeID: &nodeB.ID, DiscordNodeID: &nodeB.ID,
	}))
	secondRepositoryID, secondItemID, secondProfileID := seedWorkerGitHubQueue(t, db, 2)
	secondIntent := enqueueWorkerIntent(t, db, secondRepositoryID, secondItemID,
		secondProfileID, "second")
	assertPlacement(t, db, secondItemID, secondIntent, nodeB.ID, "queued")
	thirdIntent := enqueueWorkerIntent(t, db, repositoryID, firstItemID, profileID, "first-again")
	assertPlacement(t, db, firstItemID, thirdIntent, nodeA.ID, "queued")

	claimB, err := clientB.Claim(ctx, workerprotocol.ClaimRequest{WorkerID: "worker-b",
		Role: "github"})
	require.NoError(t, err)
	require.NotNil(t, claimB.Task)
	require.Equal(t, secondItemID, claimB.Task.Claimed.WorkItemID)
	claimA, err := clientA.Claim(ctx, workerprotocol.ClaimRequest{WorkerID: "worker-a",
		Role: "github"})
	require.NoError(t, err)
	require.NotNil(t, claimA.Task)
	require.Equal(t, firstItemID, claimA.Task.Claimed.WorkItemID)
	_, err = db.ExecContext(ctx, `UPDATE codex_thread_controls
		SET lease_expires_at = now() - interval '1 second' WHERE id = $1`,
		claimA.Task.Claimed.ControlID)
	require.NoError(t, err)
	requeued, err := codexcontrol.NewRepository(db, 2*time.Second).RequeueExpired(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, requeued)
	heartbeat, err := clientA.RunHeartbeat(ctx, claimA.Task)
	require.NoError(t, err)
	require.True(t, heartbeat.Recovery.Recovering,
		"远程 Run 断线后必须由原节点使用 Journal 中的 Lease 恢复")
	require.Len(t, heartbeat.Commands, 1)
	require.Equal(t, thirdIntent, heartbeat.Commands[0].ID)
	require.NoError(t, clientA.AckCommand(ctx, claimA.Task, heartbeat.Commands[0], "steer", "turn-a"))
	interruptID := enqueueWorkerOperation(t, db, repositoryID, firstItemID, profileID,
		"interrupt-a", "interrupt")
	heartbeat, err = clientA.RunHeartbeat(ctx, claimA.Task)
	require.NoError(t, err)
	require.Len(t, heartbeat.Commands, 1)
	require.Equal(t, interruptID, heartbeat.Commands[0].ID)
	require.NoError(t, clientA.AckCommand(ctx, claimA.Task, heartbeat.Commands[0], "interrupt", "turn-a"))

	event := workerprotocol.EventInput{Sequence: 1, Type: "turn.started",
		Payload: json.RawMessage(`{"state":"running"}`)}
	require.NoError(t, clientA.Events(ctx, claimA.Task, []workerprotocol.EventInput{event}))
	require.NoError(t, clientA.Events(ctx, claimA.Task, []workerprotocol.EventInput{event}),
		"重复事件必须幂等")
	require.Error(t, clientA.Events(ctx, claimA.Task, []workerprotocol.EventInput{{
		Sequence: 3, Type: "turn.delta", Payload: json.RawMessage(`{}`),
	}}), "跳号事件必须拒绝")
	require.NoError(t, clientA.Complete(ctx, claimA.Task, codexcontrol.TurnResult{
		TurnID: "turn-a", FinalAnswer: "done",
	}))
	require.NoError(t, clientA.Complete(ctx, claimA.Task, codexcontrol.TurnResult{
		TurnID: "turn-a", FinalAnswer: "done",
	}), "重复完成必须幂等")
	_, err = clientB.RunHeartbeat(ctx, claimA.Task)
	require.Error(t, err, "其他节点不能续租该 Run")
	require.Error(t, nodes.Delete(ctx, nodeA.ID), "仍被资源引用的节点不能删除")
	require.NoError(t, nodes.SetEnabled(ctx, nodeB.ID, false))
	_, err = clientB.Claim(ctx, workerprotocol.ClaimRequest{WorkerID: "worker-b", Role: "github"})
	require.Error(t, err, "禁用节点不能继续领取任务")
}

func TestWorkerAPIDiscordRuntimePreferencesFreeze(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	node, enrollment, err := server.nodes.Create(ctx, "discord-home", []string{"discord"}, 1)
	require.NoError(t, err)
	_, credential, err := server.nodes.Enroll(ctx, enrollment)
	require.NoError(t, err)
	require.NoError(t, server.nodes.SetDefaults(ctx, executionnode.Defaults{
		DiscordNodeID: &node.ID,
	}))
	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)

	repositoryID, _, profileID := seedWorkerGitHubQueue(t, db, 31)
	_, forumID := seedDevelopmentOperation(t, db, repositoryID, node.ID)
	var conversationID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_conversations
		(guild_id, forum_id, thread_id, starter_message_id, owner_discord_user_id,
		 repository_id, agent_profile_id, title, model, reasoning_effort, service_tier,
		 configuration_status, title_rename_status)
		VALUES ('worker-test-guild',$1,'runtime-thread','runtime-message-1','worker-owner',
		 $2,$3,'runtime','gpt-5.6-sol','xhigh','standard','configured','completed')
		RETURNING id`, forumID, repositoryID, profileID).Scan(&conversationID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_input_messages
		(message_id, conversation_id, discord_user_id, display_name, username,
		 access_snapshot, body) VALUES
		('runtime-message-1',$1,'worker-owner','Owner','owner','owner','first')
		RETURNING conversation_id`, conversationID).Scan(&conversationID))
	firstIntent := enqueueWorkerDiscordIntent(t, db, conversationID, "runtime-message-1",
		repositoryID, profileID)

	first, err := client.Claim(ctx, workerprotocol.ClaimRequest{WorkerID: "discord-worker",
		Role: "discord"})
	require.NoError(t, err)
	require.NotNil(t, first.Task)
	require.Equal(t, firstIntent, first.Task.Claimed.ID)
	require.Equal(t, "gpt-5.6-sol", first.Task.Snapshot.Runtime.Model)
	require.Equal(t, "xhigh", first.Task.Snapshot.Runtime.ReasoningEffort)
	require.Equal(t, "standard", first.Task.Snapshot.Runtime.ServiceTier)
	var frozenModel, frozenEffort, frozenTier string
	var frozen bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COALESCE(model,''),
		COALESCE(reasoning_effort,''), COALESCE(service_tier,''),
		runtime_preferences_frozen_at IS NOT NULL FROM codex_thread_controls
		WHERE id = $1`, first.Task.Claimed.ControlID).
		Scan(&frozenModel, &frozenEffort, &frozenTier, &frozen))
	require.Equal(t, "gpt-5.6-sol", frozenModel)
	require.Equal(t, "xhigh", frozenEffort)
	require.Equal(t, "standard", frozenTier)
	require.True(t, frozen)
	require.NoError(t, client.Complete(ctx, first.Task, codexcontrol.TurnResult{
		TurnID: "runtime-turn-1", FinalAnswer: "done",
	}))

	_, err = db.ExecContext(ctx, `UPDATE discord_conversations SET model = 'gpt-5.4',
		reasoning_effort = 'low', service_tier = 'fast' WHERE id = $1`, conversationID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_input_messages
		(message_id, conversation_id, discord_user_id, display_name, username,
		 access_snapshot, body) VALUES
		('runtime-message-2',$1,'worker-owner','Owner','owner','owner','second')`, conversationID)
	require.NoError(t, err)
	secondIntent := enqueueWorkerDiscordIntent(t, db, conversationID, "runtime-message-2",
		repositoryID, profileID)
	second, err := client.Claim(ctx, workerprotocol.ClaimRequest{WorkerID: "discord-worker",
		Role: "discord"})
	require.NoError(t, err)
	require.NotNil(t, second.Task)
	require.Equal(t, secondIntent, second.Task.Claimed.ID)
	require.Equal(t, first.Task.Claimed.ControlID, second.Task.Claimed.ControlID)
	require.Equal(t, "gpt-5.6-sol", second.Task.Snapshot.Runtime.Model)
	require.Equal(t, "xhigh", second.Task.Snapshot.Runtime.ReasoningEffort)
	require.Equal(t, "standard", second.Task.Snapshot.Runtime.ServiceTier)
}

func TestWorkerAPIDesktopThreadEventuallyBindsDiscordPost(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	node, enrollment, err := server.nodes.Create(ctx, "desktop-node", []string{"discord"}, 2)
	require.NoError(t, err)
	_, credential, err := server.nodes.Enroll(ctx, enrollment)
	require.NoError(t, err)
	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)
	repositoryID, _, _ := seedWorkerGitHubQueue(t, db, 41)
	environmentID, forumID := seedDevelopmentOperation(t, db, repositoryID, node.ID)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_members
		(guild_id, discord_user_id, username, display_name)
		VALUES ('worker-test-guild','desktop-user','desktop','Desktop Alice')`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE discord_development_environments SET
		ssh_public_key='ssh-ed25519 test', ssh_fingerprint='SHA256:test', ssh_port=2222,
		ssh_discord_user_id='desktop-user', status='ready', container_id='desktop-container'
		WHERE id=$1`, environmentID)
	require.NoError(t, err)
	manifests, err := client.DevelopmentEnvironments(ctx)
	require.NoError(t, err)
	require.Len(t, manifests, 1)
	require.NotNil(t, manifests[0].SSHParticipant)
	require.Equal(t, "desktop-user", manifests[0].SSHParticipant.DiscordUserID)
	require.Equal(t, "Desktop Alice", manifests[0].SSHParticipant.DisplayName)
	require.Equal(t, participantidentity.ID("worker-test-guild", "desktop-user"),
		manifests[0].SSHParticipant.ParticipantID)
	workspace := "/var/lib/tyrs-hand/workspaces/" + forumID.String()

	state, err := client.PrepareDesktopThread(ctx, workerprotocol.DesktopThreadPrepareRequest{
		EnvironmentID: environmentID, Operation: "start", RequestKey: strings.Repeat("a", 64),
		Params: json.RawMessage(`{"cwd":"` + workspace + `/nested","model":"mock-model","effort":"high"}`),
	})
	if err != nil {
		var requestID uuid.UUID
		require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM desktop_thread_requests
			WHERE environment_id = $1 AND request_key = $2`, environmentID, strings.Repeat("a", 64)).Scan(&requestID))
		testContext, _ := gin.CreateTestContext(httptest.NewRecorder())
		testContext.Request = httptest.NewRequest("GET", "/", nil)
		testContext.Set(workerNodeContextKey, node)
		_, directErr := server.loadDesktopThreadState(testContext, requestID)
		require.NoError(t, directErr)
	}
	require.NoError(t, err)
	require.Equal(t, "preparing", state.Status)
	var controls int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM codex_thread_controls`).Scan(&controls))
	require.Zero(t, controls)

	response := json.RawMessage(`{"thread":{"id":"codex-desktop-thread"}}`)
	state, err = client.CompleteDesktopThread(ctx, state.ID,
		workerprotocol.DesktopThreadCompleteRequest{EnvironmentID: environmentID, Response: response})
	require.NoError(t, err)
	require.Equal(t, "waiting_for_input", state.Status)
	require.NotEqual(t, uuid.Nil, state.ControlID)
	require.Equal(t, "mock-model", state.Config.Model)
	require.Equal(t, "high", state.Config.ReasoningEffort)
	require.Equal(t, "codex-desktop-thread", state.ExternalThreadID)
	var boundEnvironment uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT development_environment_id
		FROM codex_thread_controls WHERE id = $1`, state.ControlID).Scan(&boundEnvironment))
	require.Equal(t, environmentID, boundEnvironment)
	require.NoError(t, client.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
		EnvironmentID: environmentID, Generation: 10,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 1, Name: "首条输入前的正式标题",
		}},
	}))

	fork, err := client.PrepareDesktopThread(ctx, workerprotocol.DesktopThreadPrepareRequest{
		EnvironmentID: environmentID, Operation: "fork", RequestKey: strings.Repeat("b", 64),
		Params: json.RawMessage(`{"threadId":"codex-desktop-thread"}`),
	})
	require.NoError(t, err, "Fork 不应依赖源 Thread 已经创建 Discord Conversation")
	require.Equal(t, "preparing", fork.Status)
	fork, err = client.CompleteDesktopThread(ctx, fork.ID,
		workerprotocol.DesktopThreadCompleteRequest{EnvironmentID: environmentID,
			Response: json.RawMessage(`{"thread":{"id":"codex-desktop-fork"}}`)})
	require.NoError(t, err)
	require.Equal(t, "waiting_for_input", fork.Status)
	require.Equal(t, state.Config, fork.Config)

	task, err := client.PrepareDesktopTurn(ctx, workerprotocol.DesktopTurnPrepareRequest{
		EnvironmentID: environmentID, WorkerID: "desktop-worker",
		RequestKey: strings.Repeat("d", 64), Params: json.RawMessage(
			`{"threadId":"codex-desktop-thread","clientUserMessageId":"desktop-client-message-1",` +
				`"input":[{"type":"text","text":"desktop asks"}]}`),
	})
	require.NoError(t, err)
	require.Equal(t, "desktop", task.Claimed.InputSurface)
	require.Empty(t, task.Claimed.DiscordMessageID)
	require.NotNil(t, task.Snapshot.Discord)
	require.Equal(t, "desktop asks", task.Snapshot.Discord.Body)
	require.Equal(t, "desktop-user", task.Snapshot.Discord.UserID)
	require.Equal(t, "Desktop Alice", task.Snapshot.Discord.DisplayName)
	require.Equal(t, participantidentity.ID("worker-test-guild", "desktop-user"),
		task.Claimed.ActorParticipantID)
	require.Equal(t, "Desktop Alice", task.Claimed.ActorDisplayName)
	outbox := discordintegration.NewSQLoutbox(db)
	item, err := outbox.Claim(ctx, time.Minute)
	require.NoError(t, err)
	require.NotNil(t, item)
	require.Equal(t, "desktop-thread-post:"+state.ID.String(), item.OperationKey)
	require.Contains(t, string(item.Payload), "Desktop Alice")
	require.Contains(t, string(item.Payload), "desktop asks")
	require.Contains(t, string(item.Payload), "首条输入前的正式标题")
	require.NoError(t, outbox.Complete(ctx, *item,
		json.RawMessage(`{"threadId":"desktop-discord-thread","messageId":"desktop-starter"}`)))
	state, err = client.DesktopThreadState(ctx, state.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", state.Status)
	require.NotEqual(t, uuid.Nil, state.ConversationID)
	var initialAppliedName string
	var initialAppliedRevision int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COALESCE(applied_thread_name,''),
		applied_thread_name_revision FROM codex_thread_controls WHERE id=$1`,
		state.ControlID).Scan(&initialAppliedName, &initialAppliedRevision))
	require.Equal(t, "首条输入前的正式标题", initialAppliedName)
	require.Equal(t, int64(1), initialAppliedRevision,
		"Forum Post 首次创建已应用正式标题时应同步 applied revision")
	var firstIntentProjectionKey, firstRequestProjectionKey string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT intent.desktop_input_projection_key,
		request.first_input_projection_key FROM codex_turn_intents intent
		JOIN desktop_thread_requests request ON request.control_id=intent.control_id
		WHERE intent.id=$1`, task.Claimed.ID).
		Scan(&firstIntentProjectionKey, &firstRequestProjectionKey))
	require.Equal(t, "desktop-client-message-1", firstIntentProjectionKey)
	require.Equal(t, firstIntentProjectionKey, firstRequestProjectionKey)
	require.NoError(t, client.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
		EnvironmentID: environmentID, Generation: 10,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 2, Name: "WakeQora 正式标题",
		}},
	}))
	require.NoError(t, client.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
		EnvironmentID: environmentID, Generation: 10,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 1, Name: "迟到的旧标题",
		}},
	}))
	var desiredName, conversationTitle string
	var desiredRevision int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT ct.desired_thread_name,
		ct.desired_thread_name_revision, c.title FROM codex_thread_controls ct
		JOIN discord_conversations c ON c.id = ct.discord_conversation_id
		WHERE ct.id = $1`, state.ControlID).
		Scan(&desiredName, &desiredRevision, &conversationTitle))
	require.Equal(t, "WakeQora 正式标题", desiredName)
	require.Equal(t, int64(2), desiredRevision)
	require.Equal(t, desiredName, conversationTitle)
	var renamePayload string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT payload::text FROM integration_outbox
		WHERE operation_key = $1`, "thread-name:"+state.ControlID.String()).Scan(&renamePayload))
	require.Contains(t, renamePayload, "WakeQora 正式标题")
	require.Contains(t, renamePayload, `"revision": 2`)
	var renameItem *discordintegration.OutboxItem
	for {
		renameItem, err = outbox.Claim(ctx, time.Minute)
		require.NoError(t, err)
		require.NotNil(t, renameItem)
		if renameItem.OperationKey == "thread-name:"+state.ControlID.String() {
			break
		}
		require.NoError(t, outbox.Retry(ctx, *renameItem, time.Now().Add(time.Hour),
			errors.New("推迟无关投影")))
	}
	require.NoError(t, client.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
		EnvironmentID: environmentID, Generation: 10,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 3, Name: "竞争后的最新标题",
		}},
	}))
	require.NoError(t, outbox.Complete(ctx, *renameItem, json.RawMessage(`{}`)))
	var appliedName, renameStatus string
	var appliedRevision int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT desired_thread_name,
		desired_thread_name_revision, COALESCE(applied_thread_name,''),
		applied_thread_name_revision FROM codex_thread_controls WHERE id=$1`,
		state.ControlID).Scan(&desiredName, &desiredRevision, &appliedName, &appliedRevision))
	require.Equal(t, "竞争后的最新标题", desiredName)
	require.Equal(t, int64(3), desiredRevision)
	require.Less(t, appliedRevision, desiredRevision,
		"旧 rename 完成回调不得把新 revision 标记为已应用")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status, payload::text
		FROM integration_outbox WHERE operation_key=$1`,
		"thread-name:"+state.ControlID.String()).Scan(&renameStatus, &renamePayload))
	require.Equal(t, "pending", renameStatus)
	require.Contains(t, renamePayload, "竞争后的最新标题")
	require.Contains(t, renamePayload, `"revision": 3`)
	_, err = db.ExecContext(ctx, `UPDATE integration_outbox SET available_at=now()
		WHERE operation_key=$1`, "thread-name:"+state.ControlID.String())
	require.NoError(t, err)
	renameItem, err = outbox.Claim(ctx, time.Minute)
	require.NoError(t, err)
	require.NotNil(t, renameItem)
	require.NoError(t, outbox.Complete(ctx, *renameItem, json.RawMessage(`{}`)))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COALESCE(applied_thread_name,''),
		applied_thread_name_revision FROM codex_thread_controls WHERE id=$1`,
		state.ControlID).Scan(&appliedName, &appliedRevision))
	require.Equal(t, desiredName, appliedName)
	require.Equal(t, desiredRevision, appliedRevision)
	require.NoError(t, client.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
		EnvironmentID: environmentID, Generation: 10,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 4, Name: "即将失败的旧标题",
		}},
	}))
	_, err = db.ExecContext(ctx, `UPDATE integration_outbox SET available_at=now()
		WHERE operation_key=$1`, "thread-name:"+state.ControlID.String())
	require.NoError(t, err)
	renameItem, err = outbox.Claim(ctx, time.Minute)
	require.NoError(t, err)
	require.NotNil(t, renameItem)
	require.Equal(t, "thread-name:"+state.ControlID.String(), renameItem.OperationKey)
	require.NoError(t, client.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
		EnvironmentID: environmentID, Generation: 10,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 5, Name: "失败竞争后的最新标题",
		}},
	}))
	require.NoError(t, outbox.Fail(ctx, *renameItem, errors.New("旧 rename 失败")))
	var lastNameError sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status, payload::text
		FROM integration_outbox WHERE operation_key=$1`,
		"thread-name:"+state.ControlID.String()).Scan(&renameStatus, &renamePayload))
	require.Equal(t, "pending", renameStatus)
	require.Contains(t, renamePayload, "失败竞争后的最新标题")
	require.Contains(t, renamePayload, `"revision": 5`)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT thread_name_last_error
		FROM codex_thread_controls WHERE id=$1`, state.ControlID).Scan(&lastNameError))
	require.False(t, lastNameError.Valid,
		"旧 rename 失败回调不得给更新 revision 写入错误")

	require.NoError(t, client.RecordSubmission(ctx, &task, "desktop-turn-1"))
	require.NoError(t, client.ConfirmTurn(ctx, &task, "desktop-turn-1"))
	require.NoError(t, client.Events(ctx, &task, []workerprotocol.EventInput{
		{Sequence: 1, Type: "item/completed", Payload: json.RawMessage(
			`{"item":{"id":"desktop-user-item-1","type":"userMessage",` +
				`"clientId":"desktop-client-message-1"}}`)},
		{Sequence: 2, Type: "item/started",
			Payload: json.RawMessage(`{"item":{"id":"desktop-command","type":"commandExecution"}}`)},
	}))
	var intentUserItem, requestUserItem string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT i.codex_user_message_item_id,
		r.codex_user_message_item_id FROM codex_turn_intents i
		JOIN desktop_thread_requests r ON r.control_id=i.control_id WHERE i.id=$1`,
		task.Claimed.ID).Scan(&intentUserItem, &requestUserItem))
	require.Equal(t, "desktop-user-item-1", intentUserItem)
	require.Equal(t, intentUserItem, requestUserItem)
	var timelineProjection, emptyAnchorProjection int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_projections
		WHERE projection_key = $1`, "conversation:"+state.ConversationID.String()+
		":message:desktop-"+task.Claimed.ID.String()).Scan(&timelineProjection))
	require.Equal(t, 1, timelineProjection,
		"Desktop timeline-only 事件必须复用该 Intent 的投影锚点")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_projections
		WHERE projection_key = $1`, "conversation:"+state.ConversationID.String()+
		":message:").Scan(&emptyAnchorProjection))
	require.Zero(t, emptyAnchorProjection, "Desktop timeline-only 事件不得创建空锚点 Projection")
	steerRequest := workerprotocol.DesktopSteerRecordRequest{
		EnvironmentID: environmentID, RequestKey: strings.Repeat("f", 64),
		Params: json.RawMessage(`{"threadId":"codex-desktop-thread",` +
			`"expectedTurnId":"desktop-turn-1",` +
			`"clientUserMessageId":"desktop-client-steer-1",` +
			`"input":[{"type":"text","text":"desktop follows up"}]}`),
	}
	require.NoError(t, client.RecordDesktopSteer(ctx, steerRequest))
	require.NoError(t, client.RecordDesktopSteer(ctx, steerRequest),
		"Desktop Steer 重试不得重复创建 Intent")
	var steerInputProjection int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM integration_outbox
		WHERE operation_key LIKE $1`, "desktop-input:"+state.ConversationID.String()+":"+
		"desktop-client-steer-1:%").Scan(&steerInputProjection))
	require.Equal(t, 1, steerInputProjection)
	var steerIntentID, steerParticipantID uuid.UUID
	var steerStatus, steerDisplayName, steerProjectionStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id, status, actor_participant_id,
		actor_display_name, desktop_input_projection_status
		FROM codex_turn_intents WHERE idempotency_key=$1`,
		"desktop-steer:"+environmentID.String()+":"+strings.Repeat("f", 64)).
		Scan(&steerIntentID, &steerStatus, &steerParticipantID, &steerDisplayName,
			&steerProjectionStatus))
	require.Equal(t, "running", steerStatus)
	require.Equal(t, "projected", steerProjectionStatus)
	require.Equal(t, participantidentity.ID("worker-test-guild", "desktop-user"),
		steerParticipantID)
	require.Equal(t, "Desktop Alice", steerDisplayName)
	require.NoError(t, client.Events(ctx, &task, []workerprotocol.EventInput{{
		Sequence: 3, Type: "item/completed", Payload: json.RawMessage(
			`{"item":{"id":"desktop-steer-item-1","type":"userMessage",` +
				`"clientId":"desktop-client-steer-1"}}`),
	}}))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT codex_user_message_item_id
		FROM codex_turn_intents WHERE id=$1`, steerIntentID).Scan(&intentUserItem))
	require.Equal(t, "desktop-steer-item-1", intentUserItem)
	interactive, err := client.RegisterInteractive(ctx, &task, json.RawMessage(`"input-1"`),
		json.RawMessage(`{"threadId":"codex-desktop-thread","turnId":"desktop-turn-1",`+
			`"itemId":"question-1","questions":[{"id":"choice","header":"Choose",`+
			`"question":"Continue?","options":[{"label":"Yes","description":"Continue"},`+
			`{"label":"No","description":"Stop"}]}],"autoResolutionMs":60000}`), 1)
	require.NoError(t, err)
	require.Equal(t, "pending", interactive.Status)
	require.False(t, interactive.Ready)
	var activeSlot sql.NullInt64
	var runStatus, intentStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT r.active_slot, r.status, i.status
		FROM codex_turn_runs r JOIN codex_turn_intents i ON i.id=r.primary_intent_id
		WHERE r.id=$1`, task.Claimed.RunID).Scan(&activeSlot, &runStatus, &intentStatus))
	require.False(t, activeSlot.Valid, "等待用户回答时必须释放计算槽")
	require.Equal(t, "waiting_for_user", runStatus)
	require.Equal(t, "waiting_for_user", intentStatus)
	answered, err := client.AnswerInteractive(ctx, workerprotocol.InteractiveAnswerRequest{
		EnvironmentID: environmentID, ThreadID: "codex-desktop-thread", TurnID: "desktop-turn-1",
		ItemID: "question-1", Surface: "discord",
		Answer: json.RawMessage(`{"answers":{"choice":{"answers":["Yes"]}}}`),
	})
	require.NoError(t, err)
	require.True(t, answered.Accepted)
	require.True(t, answered.Ready, "回答获胜后应在有空闲槽时恢复运行")
	duplicate, err := client.AnswerInteractive(ctx, workerprotocol.InteractiveAnswerRequest{
		EnvironmentID: environmentID, ThreadID: "codex-desktop-thread", TurnID: "desktop-turn-1",
		ItemID: "question-1", Surface: "desktop",
		Answer: json.RawMessage(`{"answers":{"choice":{"answers":["No"]}}}`),
	})
	require.NoError(t, err)
	require.False(t, duplicate.Accepted, "并发或重复回答必须 first-answer-wins")
	require.JSONEq(t, string(answered.Answer), string(duplicate.Answer))

	secretInput, err := client.RegisterInteractive(ctx, &task, json.RawMessage(`"input-secret"`),
		json.RawMessage(`{"threadId":"codex-desktop-thread","turnId":"desktop-turn-1",`+
			`"itemId":"question-secret","questions":[{"id":"token","header":"Secret",`+
			`"question":"Token?","isSecret":true}],"autoResolutionMs":60000}`), 1)
	require.NoError(t, err)
	require.True(t, secretInput.Secret)
	secretAnswer := json.RawMessage(`{"answers":{"token":{"answers":["not-plaintext-secret"]}}}`)
	_, err = client.AnswerInteractive(ctx, workerprotocol.InteractiveAnswerRequest{
		EnvironmentID: environmentID, ThreadID: "codex-desktop-thread", TurnID: "desktop-turn-1",
		ItemID: "question-secret", Surface: "discord", Answer: secretAnswer,
	})
	require.Error(t, err, "Secret 回答不得从 Discord 提交")
	secretState, err := client.AnswerInteractive(ctx, workerprotocol.InteractiveAnswerRequest{
		EnvironmentID: environmentID, ThreadID: "codex-desktop-thread", TurnID: "desktop-turn-1",
		ItemID: "question-secret", Surface: "desktop", Answer: secretAnswer,
	})
	require.NoError(t, err)
	require.True(t, secretState.Accepted)
	require.JSONEq(t, string(secretAnswer), string(secretState.Answer))
	var plainAnswer sql.NullString
	var ciphertext []byte
	require.NoError(t, db.QueryRowContext(ctx, `SELECT q.answer::text, es.ciphertext
		FROM codex_interactive_requests q JOIN encrypted_secrets es ON es.id=q.answer_secret_id
		WHERE q.id=$1`, secretInput.ID).Scan(&plainAnswer, &ciphertext))
	require.False(t, plainAnswer.Valid)
	require.NotContains(t, string(ciphertext), "not-plaintext-secret")

	timed, err := client.RegisterInteractive(ctx, &task, json.RawMessage(`"input-timeout"`),
		json.RawMessage(`{"threadId":"codex-desktop-thread","turnId":"desktop-turn-1",`+
			`"itemId":"question-timeout","questions":[{"id":"late","header":"Wait",`+
			`"question":"Answer?"}],"autoResolutionMs":1}`), 1)
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)
	timed, err = client.InteractiveState(ctx, timed.ID)
	require.NoError(t, err)
	require.Equal(t, "expired", timed.Status)
	require.True(t, timed.Ready)
	require.JSONEq(t, `{"answers":{}}`, string(timed.Answer))
	interrupted, err := client.RegisterInteractive(ctx, &task, json.RawMessage(`"input-restart"`),
		json.RawMessage(`{"threadId":"codex-desktop-thread","turnId":"desktop-turn-1",`+
			`"itemId":"question-restart","questions":[{"id":"restart","header":"Restart",`+
			`"question":"Still there?"}],"autoResolutionMs":60000}`), 1)
	require.NoError(t, err)
	require.NoError(t, client.InterruptEnvironmentInteractive(ctx, environmentID))
	interrupted, err = client.InteractiveState(ctx, interrupted.ID)
	require.NoError(t, err)
	require.Equal(t, "interrupted", interrupted.Status)
	require.False(t, interrupted.Ready)
	require.NoError(t, client.Events(ctx, &task, []workerprotocol.EventInput{{
		Sequence: 4, Type: "discord.progress",
		Payload: json.RawMessage(`{"state":"running","detail":"Desktop running"}`),
	}}))
	require.NoError(t, client.Complete(ctx, &task, codexcontrol.TurnResult{
		TurnID: "desktop-turn-1", FinalAnswer: "desktop done",
	}))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM codex_turn_intents
		WHERE id=$1`, steerIntentID).Scan(&steerStatus))
	require.Equal(t, "completed", steerStatus)
	var projectedReply int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM integration_outbox
		WHERE operation_key = $1`, "conversation-reply:"+state.ConversationID.String()+
		":message:desktop-"+task.Claimed.ID.String()).Scan(&projectedReply))
	require.Equal(t, 1, projectedReply)

	forkTask, err := client.PrepareDesktopTurn(ctx, workerprotocol.DesktopTurnPrepareRequest{
		EnvironmentID: environmentID, WorkerID: "desktop-worker",
		RequestKey: strings.Repeat("8", 64), Params: json.RawMessage(
			`{"threadId":"codex-desktop-fork","clientUserMessageId":"desktop-fork-message-1",` +
				`"input":[{"type":"text","text":"fork first input"}]}`),
	})
	require.NoError(t, err)
	require.Equal(t, fork.ControlID, forkTask.Claimed.ControlID)
	require.Equal(t, task.Snapshot.Runtime.Model, forkTask.Snapshot.Runtime.Model)
	require.Equal(t, task.Snapshot.Runtime.ReasoningEffort,
		forkTask.Snapshot.Runtime.ReasoningEffort)
	var forkPost *discordintegration.OutboxItem
	for {
		forkPost, err = outbox.Claim(ctx, time.Minute)
		require.NoError(t, err)
		require.NotNil(t, forkPost)
		if forkPost.OperationKey == "desktop-thread-post:"+fork.ID.String() {
			break
		}
		require.NoError(t, outbox.Retry(ctx, *forkPost, time.Now().Add(time.Hour),
			errors.New("推迟无关投影")))
	}
	require.Contains(t, string(forkPost.Payload), "Desktop Alice")
	require.Contains(t, string(forkPost.Payload), "fork first input")
	require.NoError(t, outbox.Complete(ctx, *forkPost,
		json.RawMessage(`{"threadId":"desktop-discord-fork","messageId":"desktop-fork-starter"}`)))
	fork, err = client.DesktopThreadState(ctx, fork.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", fork.Status)
	require.NotEqual(t, state.ConversationID, fork.ConversationID)
	require.NoError(t, client.RecordSubmission(ctx, &forkTask, "desktop-fork-turn-1"))
	require.NoError(t, client.ConfirmTurn(ctx, &forkTask, "desktop-fork-turn-1"))
	require.NoError(t, client.Complete(ctx, &forkTask, codexcontrol.TurnResult{
		TurnID: "desktop-fork-turn-1", FinalAnswer: "fork done",
	}))

	_, err = db.ExecContext(ctx, `UPDATE discord_development_environments SET
		ssh_public_key=NULL, ssh_fingerprint=NULL, ssh_port=NULL, ssh_discord_user_id=NULL
		WHERE id=$1`, environmentID)
	require.NoError(t, err)
	unboundTask, err := client.PrepareDesktopTurn(ctx, workerprotocol.DesktopTurnPrepareRequest{
		EnvironmentID: environmentID, WorkerID: "desktop-worker",
		RequestKey: strings.Repeat("e", 64), Params: json.RawMessage(
			`{"threadId":"codex-desktop-thread","input":[{"type":"text","text":"local desktop"}]}`),
	})
	require.NoError(t, err)
	require.Equal(t, uuid.Nil, unboundTask.Claimed.ActorParticipantID)
	require.Empty(t, unboundTask.Claimed.ActorDisplayName)
	require.Empty(t, unboundTask.Snapshot.Discord.UserID)
	require.Empty(t, unboundTask.Snapshot.Discord.DisplayName)
	require.Empty(t, unboundTask.Snapshot.Discord.Username)
	require.NoError(t, client.RecordSubmission(ctx, &unboundTask, "desktop-turn-2"))
	require.NoError(t, client.ConfirmTurn(ctx, &unboundTask, "desktop-turn-2"))
	require.NoError(t, client.Complete(ctx, &unboundTask, codexcontrol.TurnResult{
		TurnID: "desktop-turn-2", FinalAnswer: "local desktop done",
	}))

	failed, err := client.PrepareDesktopThread(ctx, workerprotocol.DesktopThreadPrepareRequest{
		EnvironmentID: environmentID, Operation: "start", RequestKey: strings.Repeat("c", 64),
		Params: json.RawMessage(`{"cwd":"` + workspace + `"}`),
	})
	require.NoError(t, err)
	failed, err = client.CompleteDesktopThread(ctx, failed.ID,
		workerprotocol.DesktopThreadCompleteRequest{EnvironmentID: environmentID,
			Response: json.RawMessage(`{"thread":{"id":"codex-desktop-failed"}}`)})
	require.NoError(t, err)
	failedTask, err := client.PrepareDesktopTurn(ctx, workerprotocol.DesktopTurnPrepareRequest{
		EnvironmentID: environmentID, WorkerID: "desktop-worker",
		RequestKey: strings.Repeat("9", 64), Params: json.RawMessage(
			`{"threadId":"codex-desktop-failed","clientUserMessageId":"offline-first",` +
				`"input":[{"type":"text","text":"offline post"}]}`),
	})
	require.NoError(t, err)
	require.NotNil(t, failedTask.Snapshot.Discord)
	for {
		item, err = outbox.Claim(ctx, time.Minute)
		require.NoError(t, err)
		require.NotNil(t, item)
		if item.OperationKey == "desktop-thread-post:"+failed.ID.String() {
			break
		}
		require.NoError(t, outbox.Fail(ctx, *item, errors.New("skip fork post")))
	}
	require.NoError(t, outbox.Fail(ctx, *item, errors.New("discord unavailable")))
	failed, err = client.DesktopThreadState(ctx, failed.ID)
	require.NoError(t, err)
	require.Equal(t, "post_failed", failed.Status)
	require.NoError(t, client.RecordSubmission(ctx, &failedTask, "desktop-offline-turn-1"))
	require.NoError(t, client.ConfirmTurn(ctx, &failedTask, "desktop-offline-turn-1"))
	require.NoError(t, client.Events(ctx, &failedTask, []workerprotocol.EventInput{{
		Sequence: 1, Type: "discord.progress",
		Payload: json.RawMessage(`{"state":"running","detail":"offline running"}`),
	}}))
	require.NoError(t, client.Complete(ctx, &failedTask, codexcontrol.TurnResult{
		TurnID: "desktop-offline-turn-1", FinalAnswer: "offline first done",
	}))

	recoveryTask, err := client.PrepareDesktopTurn(ctx, workerprotocol.DesktopTurnPrepareRequest{
		EnvironmentID: environmentID, WorkerID: "desktop-worker",
		RequestKey: strings.Repeat("7", 64), Params: json.RawMessage(
			`{"threadId":"codex-desktop-failed","clientUserMessageId":"offline-second",` +
				`"input":[{"type":"text","text":"second while recovering"}]}`),
	})
	require.NoError(t, err)
	var recoveredPost *discordintegration.OutboxItem
	for {
		recoveredPost, err = outbox.Claim(ctx, time.Minute)
		require.NoError(t, err)
		require.NotNil(t, recoveredPost)
		if recoveredPost.OperationKey == "desktop-thread-post:"+failed.ID.String() {
			break
		}
		require.NoError(t, outbox.Retry(ctx, *recoveredPost, time.Now().Add(time.Hour),
			errors.New("推迟无关投影")))
	}
	require.Contains(t, string(recoveredPost.Payload), "offline post")
	require.NotContains(t, string(recoveredPost.Payload), "second while recovering",
		"重试创建 Forum Post 必须保留最初的 Starter Message")
	require.NoError(t, outbox.Complete(ctx, *recoveredPost,
		json.RawMessage(`{"threadId":"desktop-discord-recovered",`+
			`"messageId":"desktop-recovered-starter"}`)))
	failed, err = client.DesktopThreadState(ctx, failed.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", failed.Status)
	var recoveredSecondInput, recoveredFirstReply int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM integration_outbox
		WHERE operation_key LIKE $1`, "desktop-input:"+failed.ConversationID.String()+
		":offline-second:%").Scan(&recoveredSecondInput))
	require.Equal(t, 1, recoveredSecondInput,
		"Post 恢复后必须补投影故障期间的后续 Desktop 输入")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM integration_outbox
		WHERE operation_key=$1`, "conversation-reply:"+failed.ConversationID.String()+
		":message:desktop-"+failedTask.Claimed.ID.String()).Scan(&recoveredFirstReply))
	require.Equal(t, 1, recoveredFirstReply,
		"Post 恢复后必须补投影已经完成的最终回复")
	require.NoError(t, client.RecordSubmission(ctx, &recoveryTask, "desktop-offline-turn-2"))
	require.NoError(t, client.ConfirmTurn(ctx, &recoveryTask, "desktop-offline-turn-2"))
	require.NoError(t, client.Complete(ctx, &recoveryTask, codexcontrol.TurnResult{
		TurnID: "desktop-offline-turn-2", FinalAnswer: "offline second done",
	}))
}

func TestDiscordDevelopmentEnvironmentSSHAPIBindsParticipantAndRedactsAudit(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	nodes := executionnode.NewService(db)
	node, _, err := nodes.Create(ctx, "ssh-api-node", []string{"discord"}, 2)
	require.NoError(t, err)
	repositoryID, _, _ := seedWorkerGitHubQueue(t, db, 51)
	environmentID, _ := seedDevelopmentOperation(t, db, repositoryID, node.ID)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_members
		(guild_id, discord_user_id, username, display_name)
		VALUES ('worker-test-guild','100000000000000009','desktop','Desktop Member')`)
	require.NoError(t, err)
	public, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshKey, err := ssh.NewPublicKey(public)
	require.NoError(t, err)
	publicKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshKey)))
	server := &Server{db: db, discord: discordintegration.NewManager(db, nil), logger: zap.NewNop()}
	request := func(method string, payload any) (*gin.Context, *httptest.ResponseRecorder) {
		t.Helper()
		var body []byte
		if payload != nil {
			body, err = json.Marshal(payload)
			require.NoError(t, err)
		}
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(method, "/discord/development-environments/"+
			environmentID.String()+"/ssh", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: environmentID.String()}}
		return c, recorder
	}

	c, _ := request(http.MethodPut, map[string]any{
		"publicKey": publicKey, "port": 2222, "discordUserId": "100000000000000009",
	})
	server.putDiscordDevelopmentEnvironmentSSH(c)
	require.Equal(t, http.StatusAccepted, c.Writer.Status())
	var savedUserID string
	var revision int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT ssh_discord_user_id,
		ssh_config_revision FROM discord_development_environments WHERE id=$1`,
		environmentID).Scan(&savedUserID, &revision))
	require.Equal(t, "100000000000000009", savedUserID)
	require.Equal(t, int64(1), revision)
	var auditMetadata string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT metadata::text FROM audit_logs
		WHERE action='discord.development_environment.ssh.update'
		ORDER BY created_at DESC LIMIT 1`).Scan(&auditMetadata))
	require.Contains(t, auditMetadata, "100000000000000009")
	require.Contains(t, auditMetadata, "SHA256:")
	require.NotContains(t, auditMetadata, publicKey)

	listRecorder := httptest.NewRecorder()
	listContext, _ := gin.CreateTestContext(listRecorder)
	listContext.Request = httptest.NewRequest(http.MethodGet,
		"/discord/development-environments", nil)
	server.listDiscordDevelopmentEnvironments(listContext)
	require.Equal(t, http.StatusOK, listRecorder.Code)
	var environments []discordintegration.DevelopmentEnvironment
	require.NoError(t, json.Unmarshal(listRecorder.Body.Bytes(), &environments))
	require.Len(t, environments, 1)
	require.Equal(t, "100000000000000009", environments[0].SSHDiscordUserID)
	require.Equal(t, "Desktop Member", environments[0].SSHDisplayName)

	c, _ = request(http.MethodPut, map[string]any{
		"publicKey": publicKey, "port": 2222, "discordUserId": "100000000000000099",
	})
	server.putDiscordDevelopmentEnvironmentSSH(c)
	require.Equal(t, http.StatusConflict, c.Writer.Status())

	c, _ = request(http.MethodDelete, nil)
	server.deleteDiscordDevelopmentEnvironmentSSH(c)
	require.Equal(t, http.StatusAccepted, c.Writer.Status())
	var cleared bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT ssh_public_key IS NULL
		AND ssh_fingerprint IS NULL AND ssh_port IS NULL AND ssh_discord_user_id IS NULL,
		ssh_config_revision FROM discord_development_environments WHERE id=$1`,
		environmentID).Scan(&cleared, &revision))
	require.True(t, cleared)
	require.Equal(t, int64(2), revision)
}

func TestWorkerAPIMissingDefaultAndDevelopmentOperationRecovery(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	node, enrollment, err := server.nodes.Create(ctx, "home", []string{"github", "discord"}, 2)
	require.NoError(t, err)
	_, credential, err := server.nodes.Enroll(ctx, enrollment)
	require.NoError(t, err)
	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)

	repositoryID, itemID, profileID := seedWorkerGitHubQueue(t, db, 3)
	intentID := enqueueWorkerIntent(t, db, repositoryID, itemID, profileID, "pending")
	assertPlacement(t, db, itemID, intentID, uuid.Nil, "placement_pending")
	require.NoError(t, server.nodes.SetDefaults(ctx, executionnode.Defaults{
		GitHubNodeID: &node.ID, DiscordNodeID: &node.ID,
	}))
	assertPlacement(t, db, itemID, intentID, node.ID, "queued")

	environmentID, forumID := seedDevelopmentOperation(t, db, repositoryID, node.ID)
	var operationID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_development_operations
		(environment_id, forum_id, operation, execution_node_id)
		VALUES ($1,$2,'delete_forum',$3) RETURNING id`, environmentID, forumID, node.ID).
		Scan(&operationID))
	first, err := client.Claim(ctx, workerprotocol.ClaimRequest{WorkerID: "home-worker",
		Role: "discord"})
	require.NoError(t, err)
	require.NotNil(t, first.DevelopmentOperation)
	require.Equal(t, operationID, first.DevelopmentOperation.ID)
	firstEpoch := first.DevelopmentOperation.LeaseEpoch
	_, err = db.ExecContext(ctx, `UPDATE discord_development_operations
		SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, operationID)
	require.NoError(t, err)
	second, err := client.Claim(ctx, workerprotocol.ClaimRequest{WorkerID: "home-worker",
		Role: "discord"})
	require.NoError(t, err)
	require.Greater(t, second.DevelopmentOperation.LeaseEpoch, firstEpoch)
	require.Error(t, client.CompleteDevelopmentOperation(ctx, first.DevelopmentOperation),
		"旧 Lease 不能完成 Operation")
	require.NoError(t, client.CompleteDevelopmentOperation(ctx, second.DevelopmentOperation))
	require.NoError(t, client.CompleteDevelopmentOperation(ctx, second.DevelopmentOperation),
		"重复完成 Operation 必须幂等")
	var forumCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_forums WHERE id = $1`,
		forumID).Scan(&forumCount))
	require.Zero(t, forumCount)
	var operationStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM discord_development_operations
		WHERE id = $1`, operationID).Scan(&operationStatus))
	require.Equal(t, "completed", operationStatus)
}

func TestEnvironmentRelayRuntimeMigrationQueuesExistingEnvironmentsOnce(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, _ := workerTestServer(t, db)
	node, _, err := server.nodes.Create(ctx, "relay-migration", []string{"discord"}, 1)
	require.NoError(t, err)
	repositoryID, _, _ := seedWorkerGitHubQueue(t, db, 71)
	environmentID, _ := seedDevelopmentOperation(t, db, repositoryID, node.ID)
	_, err = db.ExecContext(ctx, `UPDATE discord_development_environments
		SET ssh_config_revision=0, daemon_status='running' WHERE id=$1`, environmentID)
	require.NoError(t, err)

	migrationSQL, err := os.ReadFile("../database/migrations/019_environment_relay_runtime.sql")
	require.NoError(t, err)
	require.NoError(t, execMigrationSQL(ctx, db, string(migrationSQL)))
	require.NoError(t, execMigrationSQL(ctx, db, string(migrationSQL)),
		"重复执行迁移不得重复创建 reconfigure Operation")

	var revision int64
	var daemonStatus string
	var operationCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT ssh_config_revision, daemon_status
		FROM discord_development_environments WHERE id=$1`, environmentID).
		Scan(&revision, &daemonStatus))
	require.EqualValues(t, 1, revision)
	require.Equal(t, "pending", daemonStatus)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*)
		FROM discord_development_operations WHERE environment_id=$1
		AND operation='reconfigure' AND status IN ('pending','running')`, environmentID).
		Scan(&operationCount))
	require.Equal(t, 1, operationCount)
}

func TestWorkerAPIReconfigureWaitsForActiveEnvironmentTurn(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	node, enrollment, err := server.nodes.Create(ctx, "relay-reconfigure", []string{"discord"}, 2)
	require.NoError(t, err)
	_, credential, err := server.nodes.Enroll(ctx, enrollment)
	require.NoError(t, err)
	require.NoError(t, server.nodes.SetDefaults(ctx, executionnode.Defaults{DiscordNodeID: &node.ID}))
	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)

	repositoryID, _, profileID := seedWorkerGitHubQueue(t, db, 72)
	environmentID, forumID := seedDevelopmentOperation(t, db, repositoryID, node.ID)
	var conversationID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_conversations
		(guild_id, forum_id, thread_id, starter_message_id, owner_discord_user_id,
		 repository_id, agent_profile_id, title, configuration_status, title_rename_status)
		VALUES ('worker-test-guild',$1,'reconfigure-thread','reconfigure-message',
		 'worker-owner',$2,$3,'reconfigure','configured','completed') RETURNING id`,
		forumID, repositoryID, profileID).Scan(&conversationID))
	_, err = db.ExecContext(ctx, `INSERT INTO discord_input_messages
		(message_id, conversation_id, discord_user_id, display_name, username,
		 access_snapshot, body) VALUES
		('reconfigure-message',$1,'worker-owner','Owner','owner','owner','run')`, conversationID)
	require.NoError(t, err)
	enqueueWorkerDiscordIntent(t, db, conversationID, "reconfigure-message", repositoryID, profileID)

	claimed, err := client.Claim(ctx, workerprotocol.ClaimRequest{
		WorkerID: "relay-worker", Role: "discord",
	})
	require.NoError(t, err)
	require.NotNil(t, claimed.Task)
	var operationID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_development_operations
		(environment_id, operation, execution_node_id) VALUES ($1,'reconfigure',$2)
		RETURNING id`, environmentID, node.ID).Scan(&operationID))

	operation, err := server.claimDevelopmentOperation(ctx, node.ID, "relay-worker")
	require.NoError(t, err)
	require.Nil(t, operation, "环境存在 active Run 时不得领取 reconfigure")
	require.NoError(t, client.Complete(ctx, claimed.Task, codexcontrol.TurnResult{
		TurnID: "reconfigure-turn", FinalAnswer: "done",
	}))
	operation, err = server.claimDevelopmentOperation(ctx, node.ID, "relay-worker")
	require.NoError(t, err)
	require.NotNil(t, operation)
	require.Equal(t, operationID, operation.ID)
}

func execMigrationSQL(ctx context.Context, db *sql.DB, statement string) error {
	_, err := db.ExecContext(ctx, statement)
	return err
}

func TestWorkerAPISSHConfigurationRotationConstraintsAndGlobalAgents(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	nodeA, enrollmentA, err := server.nodes.Create(ctx, "ssh-a", []string{"github"}, 1)
	require.NoError(t, err)
	nodeB, enrollmentB, err := server.nodes.Create(ctx, "ssh-b", []string{"github"}, 1)
	require.NoError(t, err)
	_, nodeCredential, err := server.nodes.Enroll(ctx, enrollmentA)
	require.NoError(t, err)
	client := workerprotocol.NewClient(endpoint, nodeCredential, 5*time.Second)
	_, nodeCredentialB, err := server.nodes.Enroll(ctx, enrollmentB)
	require.NoError(t, err)
	clientB := workerprotocol.NewClient(endpoint, nodeCredentialB, 5*time.Second)

	privateKeyA := testSSHPrivateKey(t, "")
	credential, err := server.ssh.CreateCredential(ctx, sshconfig.CredentialInput{
		Name: "production", PrivateKey: privateKeyA,
	})
	require.NoError(t, err)
	var ciphertext []byte
	require.NoError(t, db.QueryRowContext(ctx, `SELECT es.ciphertext FROM encrypted_secrets es
		JOIN ssh_credentials c ON c.secret_id=es.id WHERE c.id=$1`, credential.ID).Scan(&ciphertext))
	require.False(t, bytes.Contains(ciphertext, []byte("PRIVATE KEY")))
	jump, err := server.ssh.CreateHost(ctx, sshconfig.HostInput{
		Alias: "jump", Hostname: "192.0.2.1", Port: 22, Username: "ubuntu",
		CredentialID: credential.ID, ExecutionNodeIDs: []uuid.UUID{nodeA.ID},
	})
	require.NoError(t, err)
	_, err = server.ssh.CreateHost(ctx, sshconfig.HostInput{
		Alias: "wrong-node", Hostname: "192.0.2.2", Port: 22, Username: "ubuntu",
		CredentialID: credential.ID, ProxyJumpHostID: &jump.ID,
		ExecutionNodeIDs: []uuid.UUID{nodeB.ID},
	})
	require.ErrorContains(t, err, "相同的 Execution Node")
	target, err := server.ssh.CreateHost(ctx, sshconfig.HostInput{
		Alias: "target", Hostname: "192.0.2.3", Port: 2222, Username: "deploy",
		CredentialID: credential.ID, ProxyJumpHostID: &jump.ID,
		ExecutionNodeIDs: []uuid.UUID{nodeA.ID},
	})
	require.NoError(t, err)
	_, err = server.ssh.UpdateHost(ctx, jump.ID, sshconfig.HostInput{
		Alias: jump.Alias, Hostname: jump.Hostname, Port: jump.Port, Username: jump.Username,
		CredentialID: credential.ID, ProxyJumpHostID: &target.ID,
		ExecutionNodeIDs: []uuid.UUID{nodeA.ID},
	})
	require.ErrorContains(t, err, "循环")
	_, err = server.ssh.UpdateHost(ctx, jump.ID, sshconfig.HostInput{
		Alias: jump.Alias, Hostname: jump.Hostname, Port: jump.Port, Username: jump.Username,
		CredentialID: credential.ID, ExecutionNodeIDs: nil,
	})
	require.ErrorContains(t, err, "仍被已启用主机")

	configuration, etag, changed, err := client.SSHConfiguration(ctx, "")
	require.NoError(t, err)
	require.True(t, changed)
	require.Len(t, configuration.Hosts, 2)
	require.Len(t, configuration.Credentials, 1)
	require.Equal(t, strings.TrimSpace(privateKeyA), configuration.Credentials[0].PrivateKey)
	require.NotEmpty(t, etag)
	configurationB, _, changed, err := clientB.SSHConfiguration(ctx, "")
	require.NoError(t, err)
	require.True(t, changed)
	require.Empty(t, configurationB.Hosts)
	require.Empty(t, configurationB.Credentials)
	_, sameETag, changed, err := client.SSHConfiguration(ctx, etag)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, etag, sameETag)

	privateKeyB := testSSHPrivateKey(t, "rotation-passphrase")
	enabled := true
	rotated, err := server.ssh.UpdateCredential(ctx, credential.ID, sshconfig.CredentialInput{
		Name: "production", PrivateKey: privateKeyB, Passphrase: "rotation-passphrase",
		Enabled: &enabled,
	})
	require.NoError(t, err)
	require.Greater(t, rotated.Version, credential.Version)
	configuration, rotatedETag, changed, err := client.SSHConfiguration(ctx, etag)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotEqual(t, etag, rotatedETag)
	require.Equal(t, strings.TrimSpace(privateKeyB), configuration.Credentials[0].PrivateKey)
	require.Equal(t, "rotation-passphrase", configuration.Credentials[0].Passphrase)
	require.ErrorContains(t, server.ssh.DeleteCredential(ctx, credential.ID), "关联主机")

	disabled := false
	_, err = server.ssh.UpdateCredential(ctx, credential.ID, sshconfig.CredentialInput{
		Name: "production", Enabled: &disabled,
	})
	require.NoError(t, err)
	configuration, _, changed, err = client.SSHConfiguration(ctx, rotatedETag)
	require.NoError(t, err)
	require.True(t, changed)
	require.Empty(t, configuration.Hosts)
	require.Empty(t, configuration.Credentials)

	apiKey := testSSHPrivateKey(t, "api-passphrase")
	createBody, err := json.Marshal(sshconfig.CredentialInput{
		Name: "api-managed", PrivateKey: apiKey, Passphrase: "api-passphrase",
	})
	require.NoError(t, err)
	createRecorder := httptest.NewRecorder()
	createContext, _ := gin.CreateTestContext(createRecorder)
	createContext.Request = httptest.NewRequest("POST", "/api/v1/ssh/credentials",
		bytes.NewReader(createBody))
	createContext.Request.Header.Set("Content-Type", "application/json")
	server.createSSHCredential(createContext)
	require.Equal(t, 201, createRecorder.Code)
	require.NotContains(t, createRecorder.Body.String(), "PRIVATE KEY")
	require.NotContains(t, createRecorder.Body.String(), "api-passphrase")
	require.NotContains(t, createRecorder.Body.String(), "ciphertext")
	var apiCredential sshconfig.Credential
	require.NoError(t, json.Unmarshal(createRecorder.Body.Bytes(), &apiCredential))
	var secretBeforeUpdate []byte
	require.NoError(t, db.QueryRowContext(ctx, `SELECT es.ciphertext FROM encrypted_secrets es
		JOIN ssh_credentials c ON c.secret_id=es.id WHERE c.id=$1`, apiCredential.ID).
		Scan(&secretBeforeUpdate))

	updateBody, err := json.Marshal(sshconfig.CredentialInput{Name: "api-renamed", Enabled: &enabled})
	require.NoError(t, err)
	updateRecorder := httptest.NewRecorder()
	updateContext, _ := gin.CreateTestContext(updateRecorder)
	updateContext.Request = httptest.NewRequest("PUT", "/api/v1/ssh/credentials/"+apiCredential.ID.String(),
		bytes.NewReader(updateBody))
	updateContext.Request.Header.Set("Content-Type", "application/json")
	updateContext.Params = gin.Params{{Key: "id", Value: apiCredential.ID.String()}}
	server.updateSSHCredential(updateContext)
	require.Equal(t, 200, updateRecorder.Code)
	require.NotContains(t, updateRecorder.Body.String(), "PRIVATE KEY")
	var storedSecret []byte
	require.NoError(t, db.QueryRowContext(ctx, `SELECT es.ciphertext FROM encrypted_secrets es
		JOIN ssh_credentials c ON c.secret_id=es.id WHERE c.id=$1`, apiCredential.ID).Scan(&storedSecret))
	require.Equal(t, secretBeforeUpdate, storedSecret)

	deleteRecorder := httptest.NewRecorder()
	deleteContext, _ := gin.CreateTestContext(deleteRecorder)
	deleteContext.Request = httptest.NewRequest("DELETE", "/api/v1/ssh/credentials/"+apiCredential.ID.String(), nil)
	deleteContext.Params = gin.Params{{Key: "id", Value: apiCredential.ID.String()}}
	server.deleteSSHCredential(deleteContext)
	require.Equal(t, 204, deleteContext.Writer.Status())
	var auditActions []string
	rows, err := db.QueryContext(ctx, `SELECT action FROM audit_logs
		WHERE resource_id=$1 ORDER BY created_at`, apiCredential.ID.String())
	require.NoError(t, err)
	for rows.Next() {
		var action string
		require.NoError(t, rows.Scan(&action))
		auditActions = append(auditActions, action)
	}
	require.NoError(t, rows.Close())
	require.Equal(t, []string{"ssh_credential.create", "ssh_credential.update",
		"ssh_credential.delete"}, auditActions)

	repositoryID, itemID, _ := seedWorkerGitHubQueue(t, db, 80)
	_ = repositoryID
	var before int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT context_version FROM work_items WHERE id=$1`,
		itemID).Scan(&before))
	providerBefore, err := server.settings.AgentProvider(ctx)
	require.NoError(t, err)
	require.NoError(t, server.settings.SaveGlobalAgents(ctx,
		platformsettings.GlobalAgents{Content: "# Global\r\n"}))
	agents, err := server.settings.GlobalAgents(ctx)
	require.NoError(t, err)
	require.Equal(t, "# Global\n", agents.Content)
	providerAfter, err := server.settings.AgentProvider(ctx)
	require.NoError(t, err)
	require.NotEqual(t, providerBefore.ConfigSignature, providerAfter.ConfigSignature)
	var after int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT context_version FROM work_items WHERE id=$1`,
		itemID).Scan(&after))
	require.Equal(t, before+1, after)
	require.NoError(t, server.settings.SaveGlobalAgents(ctx,
		platformsettings.GlobalAgents{Content: "# Global\n"}))
	var unchanged int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT context_version FROM work_items WHERE id=$1`,
		itemID).Scan(&unchanged))
	require.Equal(t, after, unchanged)
	agentsRecorder := httptest.NewRecorder()
	agentsContext, _ := gin.CreateTestContext(agentsRecorder)
	agentsContext.Request = httptest.NewRequest("PUT", "/api/v1/settings/global-agents",
		strings.NewReader(`{"content":"# Managed through API\n"}`))
	agentsContext.Request.Header.Set("Content-Type", "application/json")
	server.putGlobalAgents(agentsContext)
	require.Equal(t, 204, agentsContext.Writer.Status())
	var globalAuditCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM audit_logs
		WHERE action='settings.global_agents.update' AND resource_id='codex.global_agents'`).
		Scan(&globalAuditCount))
	require.Equal(t, 1, globalAuditCount)
}

func testSSHPrivateKey(t *testing.T, passphrase string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(key, "integration")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(key, "integration", []byte(passphrase))
	}
	require.NoError(t, err)
	return string(pem.EncodeToMemory(block))
}

func workerTestServer(t *testing.T, db *sql.DB) (*Server, string) {
	t.Helper()
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	secretStore := secrets.NewStore(db, box)
	settings := platformsettings.NewService(db, secretStore)
	require.NoError(t, settings.SaveAgentProvider(context.Background(),
		platformsettings.AgentProviderInput{ProviderType: "api-key", APIKey: "test-key"}))
	server := &Server{cfg: config.Config{LeaseDuration: 2 * time.Second,
		CodexMaxSteersPerTurn: 5, CodexReconcileMaxAttempts: 3}, db: db,
		nodes: executionnode.NewService(db), settings: settings,
		ssh: sshconfig.NewService(db, secretStore), secrets: secretStore}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	server.registerWorkerRoutes(router)
	httpServer := httptest.NewServer(router)
	t.Cleanup(httpServer.Close)
	return server, httpServer.URL
}

func workerDatabase(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{Image: "postgres:18.3-bookworm@sha256:80630f83606d8db77d30b3851b16a9f78be2d0d4dda6f7b82a1fdca5ebe3acba",
			Env:          map[string]string{"POSTGRES_DB": "tyrs_hand", "POSTGRES_USER": "tyrs_hand", "POSTGRES_PASSWORD": "test-password"},
			ExposedPorts: []string{"5432/tcp"}, WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90 * time.Second)}, Started: true})
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

func seedWorkerGitHubQueue(t *testing.T, db *sql.DB, number int) (uuid.UUID, uuid.UUID,
	uuid.UUID,
) {
	t.Helper()
	ctx := context.Background()
	var installationID, repositoryID, itemID, profileID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO scm_installations
		(provider, external_id, account_login, account_type)
		VALUES ('github',$1,$2,'Organization') RETURNING id`, number,
		"owner-"+uuid.NewString()).Scan(&installationID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO repositories
		(installation_id, provider, external_id, owner, name, default_branch, clone_url)
		VALUES ($1,'github',$2,$3,$4,'main',$5) RETURNING id`, installationID,
		number, "owner", "repo-"+uuid.NewString(), "https://example.invalid/repo.git").
		Scan(&repositoryID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO work_items
		(repository_id, kind, external_number, title) VALUES ($1,'issue',$2,'test')
		RETURNING id`, repositoryID, number).Scan(&itemID))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM agent_profiles WHERE name = 'Default'`).
		Scan(&profileID))
	return repositoryID, itemID, profileID
}

func enqueueWorkerIntent(t *testing.T, db *sql.DB, repositoryID, itemID, profileID uuid.UUID,
	key string,
) uuid.UUID {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	intentID, inserted, err := codexcontrol.NewRepository(db, 2*time.Second).Enqueue(
		context.Background(), tx, codexcontrol.EnqueueRequest{SourceType: codexcontrol.SourceGitHub,
			WorkItemID: itemID, RepositoryID: repositoryID, AgentProfileID: profileID,
			ContextVersion: 1, IdempotencyKey: key, Instruction: "test", ReplyPolicy: "silent"})
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, tx.Commit())
	return intentID
}

func enqueueWorkerDiscordIntent(t *testing.T, db *sql.DB, conversationID uuid.UUID,
	messageID string, repositoryID, profileID uuid.UUID,
) uuid.UUID {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	intentID, inserted, err := codexcontrol.NewRepository(db, 2*time.Second).Enqueue(
		context.Background(), tx, codexcontrol.EnqueueRequest{
			SourceType: codexcontrol.SourceDiscord, DiscordConversationID: conversationID,
			DiscordMessageID: messageID, RepositoryID: repositoryID, AgentProfileID: profileID,
			ContextVersion: 1, IdempotencyKey: "discord:" + messageID,
			Instruction: messageID, ReplyPolicy: "silent", Behavior: "steer_if_active",
		})
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, tx.Commit())
	return intentID
}

func enqueueWorkerOperation(t *testing.T, db *sql.DB, repositoryID, itemID, profileID uuid.UUID,
	key, operation string,
) uuid.UUID {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	intentID, inserted, err := codexcontrol.NewRepository(db, 2*time.Second).Enqueue(
		context.Background(), tx, codexcontrol.EnqueueRequest{SourceType: codexcontrol.SourceGitHub,
			WorkItemID: itemID, RepositoryID: repositoryID, AgentProfileID: profileID,
			ContextVersion: 1, IdempotencyKey: key, Instruction: "stop", Operation: operation,
			ReplyPolicy: "silent"})
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, tx.Commit())
	return intentID
}

func assertPlacement(t *testing.T, db *sql.DB, itemID, intentID, expectedNodeID uuid.UUID,
	expectedStatus string,
) {
	t.Helper()
	var itemNode, controlNode sql.NullString
	var status string
	require.NoError(t, db.QueryRow(`SELECT w.execution_node_id::text,
		c.execution_node_id::text, i.status FROM work_items w
		JOIN codex_turn_intents i ON i.id = $2
		JOIN codex_thread_controls c ON c.id = i.control_id WHERE w.id = $1`, itemID, intentID).
		Scan(&itemNode, &controlNode, &status))
	require.Equal(t, expectedStatus, status)
	if expectedNodeID == uuid.Nil {
		require.False(t, itemNode.Valid)
		require.False(t, controlNode.Valid)
		return
	}
	require.Equal(t, expectedNodeID.String(), itemNode.String)
	require.Equal(t, expectedNodeID.String(), controlNode.String)
}

func seedDevelopmentOperation(t *testing.T, db *sql.DB, repositoryID,
	nodeID uuid.UUID,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id, enabled)
		VALUES ('worker-test-guild', true)`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_members
		(guild_id, discord_user_id, username, display_name)
		VALUES ('worker-test-guild','worker-owner','owner','Owner')`)
	require.NoError(t, err)
	var environmentID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_development_environments
		(guild_id, owner_discord_user_id, build_repository_id, container_name,
		 data_volume_name, home_volume_name, network_name, execution_node_id)
		VALUES ('worker-test-guild','worker-owner',$1,'dev-worker','dev-worker-data',
		'dev-worker-home','dev-worker-net',$2) RETURNING id`, repositoryID, nodeID).
		Scan(&environmentID))
	var resourceID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_resources
		(guild_id, resource_key, discord_id, kind, name, managed_marker)
		VALUES ('worker-test-guild','forum.worker','123456','forum','worker','marker')
		RETURNING id`).Scan(&resourceID))
	var forumID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_forums
		(guild_id, resource_id, forum_type, owner_discord_user_id, repository_id,
		 development_environment_id)
		VALUES ('worker-test-guild',$1,'development','worker-owner',$2,$3) RETURNING id`,
		resourceID, repositoryID, environmentID).Scan(&forumID))
	_, err = db.ExecContext(ctx, `INSERT INTO discord_forum_workspaces
		(forum_id, environment_id, relative_path, branch, status)
		VALUES ($1,$2,$3,'worker/test','ready')`, forumID, environmentID,
		"workspaces/"+forumID.String())
	require.NoError(t, err)
	return environmentID, forumID
}
