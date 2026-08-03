//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/participantidentity"
	"github.com/slovx2/tyrs-hand/internal/secrets"
	"github.com/slovx2/tyrs-hand/internal/security"
	platformsettings "github.com/slovx2/tyrs-hand/internal/settings"
	"github.com/slovx2/tyrs-hand/internal/sshconfig"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/slovx2/tyrs-hand/internal/workerregistry"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/ssh"
)

type fakeDesktopImageRemote struct {
	content []byte
	card    discordintegration.ComponentCardPayload
}

func (r *fakeDesktopImageRemote) UploadDesktopImage(_ context.Context, _, _ string,
	card discordintegration.ComponentCardPayload, _, _ string, source io.Reader,
) (string, error) {
	content, err := io.ReadAll(source)
	if err != nil {
		return "", err
	}
	r.content, r.card = content, card
	return "desktop-attachment", nil
}

func (r *fakeDesktopImageRemote) UpdateDesktopCard(context.Context, string, string,
	discordintegration.ComponentCardPayload,
) error {
	return nil
}

func (r *fakeDesktopImageRemote) Close(context.Context) {}

func TestWorkerAPIPlacementLeaseEventsAndIdempotency(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	workers := server.workers
	workerA, enrollmentA, err := workers.Create(ctx, "home-a", []string{"github", "discord"}, 2)
	require.NoError(t, err)
	workerB, enrollmentB, err := workers.Create(ctx, "home-b", []string{"github", "discord"}, 2)
	require.NoError(t, err)
	_, enrollmentGitHubOnly, err := workers.Create(ctx, "github-only", []string{"github"}, 1)
	require.NoError(t, err)

	clientA := workerprotocol.NewClient(endpoint, "", 5*time.Second)
	enrolledA, err := clientA.Enroll(ctx, enrollmentA)
	require.NoError(t, err)
	clientA.SetCredential(enrolledA.Credential)
	_, err = clientA.Enroll(ctx, enrollmentA)
	require.Error(t, err, "Enrollment Token 只能消费一次")
	rotationToken, err := workers.NewEnrollment(ctx, workerA.ID)
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
	_, err = clientA.Claim(ctx, workerprotocol.ClaimRequest{Role: "github"})
	require.Error(t, err, "协议不兼容时必须拒绝 Claim")
	require.NoError(t, clientA.Heartbeat(ctx, workerprotocol.HeartbeatRequest{
		WorkerVersion: "test", ProtocolVersion: workerprotocol.Version,
	}))
	_, credentialB, err := workers.Enroll(ctx, enrollmentB)
	require.NoError(t, err)
	clientB := workerprotocol.NewClient(endpoint, credentialB, 5*time.Second)
	_, githubOnlyCredential, err := workers.Enroll(ctx, enrollmentGitHubOnly)
	require.NoError(t, err)
	githubOnlyClient := workerprotocol.NewClient(endpoint, githubOnlyCredential, 5*time.Second)
	_, err = githubOnlyClient.Claim(ctx, workerprotocol.ClaimRequest{
		Role: "discord",
	})
	require.Error(t, err, "节点不能越权领取未授权角色")
	_, err = githubOnlyClient.Claim(ctx, workerprotocol.ClaimRequest{
		Role: "all",
	})
	require.Error(t, err, "all 领取要求节点同时具备 GitHub 和 Discord 角色")
	require.NoError(t, workers.SetDefaults(ctx, workerregistry.Defaults{
		GitHubWorkerID: &workerA.ID, DiscordWorkerID: &workerA.ID,
	}))

	repositoryID, firstItemID, profileID := seedWorkerGitHubQueue(t, db, 1)
	firstIntent := enqueueWorkerIntent(t, db, repositoryID, firstItemID, profileID, "first")
	assertPlacement(t, db, firstItemID, firstIntent, workerA.ID, "queued")

	require.NoError(t, workers.SetDefaults(ctx, workerregistry.Defaults{
		GitHubWorkerID: &workerB.ID, DiscordWorkerID: &workerB.ID,
	}))
	secondRepositoryID, secondItemID, secondProfileID := seedWorkerGitHubQueue(t, db, 2)
	secondIntent := enqueueWorkerIntent(t, db, secondRepositoryID, secondItemID,
		secondProfileID, "second")
	assertPlacement(t, db, secondItemID, secondIntent, workerB.ID, "queued")
	thirdIntent := enqueueWorkerIntent(t, db, repositoryID, firstItemID, profileID, "first-again")
	assertPlacement(t, db, firstItemID, thirdIntent, workerA.ID, "queued")

	claimB, err := clientB.Claim(ctx, workerprotocol.ClaimRequest{Role: "github"})
	require.NoError(t, err)
	require.NotNil(t, claimB.Task)
	require.Equal(t, secondItemID, claimB.Task.Claimed.WorkItemID)
	claimA, err := clientA.Claim(ctx, workerprotocol.ClaimRequest{Role: "all"})
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
	require.NoError(t, clientA.Events(ctx, claimA.Task, []workerprotocol.EventInput{{
		Sequence: 2, Type: "turn.delta", Payload: json.RawMessage(`{"state":"late"}`),
	}}), "终态完成后仍必须接受 Journal 补发的中间事件")
	_, err = clientB.RunHeartbeat(ctx, claimA.Task)
	require.Error(t, err, "其他节点不能续租该 Run")
	require.Error(t, workers.Delete(ctx, workerA.ID), "仍被资源引用的节点不能删除")
	require.NoError(t, workers.SetEnabled(ctx, workerB.ID, false))
	_, err = clientB.Claim(ctx, workerprotocol.ClaimRequest{Role: "github"})
	require.Error(t, err, "禁用节点不能继续领取任务")
}

func TestWorkerAPICancelFinishesAcknowledgedSteer(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	worker, enrollment, err := server.workers.Create(ctx, "cancel-steer", []string{"github"}, 1)
	require.NoError(t, err)
	_, credential, err := server.workers.Enroll(ctx, enrollment)
	require.NoError(t, err)
	require.NoError(t, server.workers.SetDefaults(ctx, workerregistry.Defaults{GitHubWorkerID: &worker.ID}))
	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)

	repositoryID, itemID, profileID := seedWorkerGitHubQueue(t, db, 91)
	primaryIntent := enqueueWorkerIntent(t, db, repositoryID, itemID, profileID, "primary")
	claimed, err := client.Claim(ctx, workerprotocol.ClaimRequest{
		Role: "github",
	})
	require.NoError(t, err)
	require.NotNil(t, claimed.Task)
	require.Equal(t, primaryIntent, claimed.Task.Claimed.ID)
	require.NoError(t, client.RecordSubmission(ctx, claimed.Task, "cancel-steer-turn"))
	require.NoError(t, client.ConfirmTurn(ctx, claimed.Task, "cancel-steer-turn"))

	steerIntent := enqueueWorkerIntent(t, db, repositoryID, itemID, profileID, "follow-up")
	heartbeat, err := client.RunHeartbeat(ctx, claimed.Task)
	require.NoError(t, err)
	require.Len(t, heartbeat.Commands, 1)
	require.Equal(t, steerIntent, heartbeat.Commands[0].ID)
	require.NoError(t, client.AckCommand(ctx, claimed.Task, heartbeat.Commands[0],
		"steer", "cancel-steer-turn"))
	require.NoError(t, client.Fail(ctx, claimed.Task, "user_interrupt", errors.New("stopped")))

	var primaryStatus, steerStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM codex_turn_intents WHERE id=$1`,
		primaryIntent).Scan(&primaryStatus))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM codex_turn_intents WHERE id=$1`,
		steerIntent).Scan(&steerStatus))
	require.Equal(t, "canceled", primaryStatus)
	require.Equal(t, "canceled", steerStatus)
}

func TestWorkerAPINonRetryableCodexErrorDoesNotRequeue(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	worker, enrollment, err := server.workers.Create(ctx, "codex-non-retry", []string{"github"}, 1)
	require.NoError(t, err)
	_, credential, err := server.workers.Enroll(ctx, enrollment)
	require.NoError(t, err)
	require.NoError(t, server.workers.SetDefaults(ctx, workerregistry.Defaults{GitHubWorkerID: &worker.ID}))
	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)

	repositoryID, itemID, profileID := seedWorkerGitHubQueue(t, db, 92)
	intentID := enqueueWorkerIntent(t, db, repositoryID, itemID, profileID, "capacity")
	claimed, err := client.Claim(ctx, workerprotocol.ClaimRequest{
		Role: "github",
	})
	require.NoError(t, err)
	require.NotNil(t, claimed.Task)
	require.Equal(t, intentID, claimed.Task.Claimed.ID)
	require.NoError(t, client.SetThread(ctx, claimed.Task, "thread-capacity"))
	require.NoError(t, client.RecordSubmission(ctx, claimed.Task, "turn-capacity"))
	require.NoError(t, client.ConfirmTurn(ctx, claimed.Task, "turn-capacity"))

	codexError := &workerprotocol.CodexTurnError{
		Message:        "Selected model is at capacity. Please try a different model.",
		CodexErrorInfo: json.RawMessage(`"serverOverloaded"`), WillRetry: false,
		ThreadID: "thread-capacity", TurnID: "turn-capacity",
	}
	require.NoError(t, client.FailWithCodexError(ctx, claimed.Task,
		"codex_non_retryable_error", codexError, codexError))

	var intentStatus, runStatus, errorType string
	var attempts int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT intent.status, intent.attempt_count,
		run.status, run.codex_error->>'codexErrorInfo'
		FROM codex_turn_intents intent JOIN codex_turn_runs run ON run.primary_intent_id=intent.id
		WHERE intent.id=$1`, intentID).Scan(&intentStatus, &attempts, &runStatus, &errorType))
	require.Equal(t, "failed", intentStatus)
	require.Equal(t, 1, attempts)
	require.Equal(t, "failed", runStatus)
	require.Equal(t, "serverOverloaded", errorType)

	next, err := client.Claim(ctx, workerprotocol.ClaimRequest{
		Role: "github",
	})
	require.NoError(t, err)
	require.Nil(t, next.Task)
}

func TestWorkerAPIDiscordRuntimePreferencesFreeze(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	worker, enrollment, err := server.workers.Create(ctx, "discord-home", []string{"discord"}, 1)
	require.NoError(t, err)
	_, credential, err := server.workers.Enroll(ctx, enrollment)
	require.NoError(t, err)
	require.NoError(t, server.workers.SetDefaults(ctx, workerregistry.Defaults{
		DiscordWorkerID: &worker.ID,
	}))
	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)

	repositoryID, _, profileID := seedWorkerGitHubQueue(t, db, 31)
	_, forumID := seedWorkerWorkspace(t, db, repositoryID, worker.ID)
	projectID := workspaceProjectIDForForum(t, db, forumID)
	var conversationID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_conversations
		(guild_id, forum_id, thread_id, starter_message_id, owner_discord_user_id,
		 workspace_project_id, agent_profile_id, title, model, reasoning_effort, service_tier,
		 collaboration_mode, configuration_status, title_rename_status)
		VALUES ('worker-test-guild',$1,'runtime-thread','runtime-message-1','worker-owner',
		 $2,$3,'runtime','gpt-5.6-sol','xhigh','standard','plan','configured','completed')
		RETURNING id`, forumID, projectID, profileID).Scan(&conversationID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_input_messages
		(message_id, conversation_id, discord_user_id, display_name, username,
		 access_snapshot, body) VALUES
		('runtime-message-1',$1,'worker-owner','Owner','owner','owner','first')
		RETURNING conversation_id`, conversationID).Scan(&conversationID))
	firstIntent := enqueueWorkerDiscordIntent(t, db, conversationID, "runtime-message-1",
		repositoryID, profileID)

	first, err := client.Claim(ctx, workerprotocol.ClaimRequest{Role: "discord"})
	require.NoError(t, err)
	require.NotNil(t, first.Task)
	require.Equal(t, firstIntent, first.Task.Claimed.ID)
	require.Equal(t, "gpt-5.6-sol", first.Task.Snapshot.Runtime.Model)
	require.Equal(t, "xhigh", first.Task.Snapshot.Runtime.ReasoningEffort)
	require.Equal(t, "standard", first.Task.Snapshot.Runtime.ServiceTier)
	require.Equal(t, "plan", first.Task.Snapshot.Runtime.CollaborationMode)
	var runMode string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT collaboration_mode FROM codex_turn_runs
		WHERE id = $1`, first.Task.Claimed.RunID).Scan(&runMode))
	require.Equal(t, "plan", runMode)
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
	appliedPayload, err := json.Marshal(workerprotocol.RuntimeSettingsApplied{
		Phase: "turn/start", Model: first.Task.Snapshot.Runtime.Model,
		ReasoningEffort:   first.Task.Snapshot.Runtime.ReasoningEffort,
		ServiceTier:       first.Task.Snapshot.Runtime.ServiceTier,
		CollaborationMode: first.Task.Snapshot.Runtime.CollaborationMode,
		SettingsRevision:  first.Task.Snapshot.Runtime.SettingsRevision,
	})
	require.NoError(t, err)
	require.NoError(t, client.Events(ctx, first.Task, []workerprotocol.EventInput{{
		Sequence: 1, Type: "runtime.settings_applied", Payload: appliedPayload,
	}}))
	var appliedModel, appliedEffort, appliedTier, appliedMode string
	var appliedRevision int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COALESCE(applied_model,''),
		COALESCE(applied_reasoning_effort,''), COALESCE(applied_service_tier,''),
		COALESCE(applied_collaboration_mode,''), applied_settings_revision
		FROM codex_turn_runs WHERE id = $1`, first.Task.Claimed.RunID).
		Scan(&appliedModel, &appliedEffort, &appliedTier, &appliedMode, &appliedRevision))
	require.Equal(t, "gpt-5.6-sol", appliedModel)
	require.Equal(t, "xhigh", appliedEffort)
	require.Equal(t, "default", appliedTier)
	require.Equal(t, "plan", appliedMode)
	require.Equal(t, first.Task.Snapshot.Runtime.SettingsRevision, appliedRevision)
	require.NoError(t, client.Complete(ctx, first.Task, codexcontrol.TurnResult{
		TurnID: "runtime-turn-1", FinalAnswer: "done",
	}))

	_, err = db.ExecContext(ctx, `UPDATE discord_conversations SET model = 'gpt-5.4',
		reasoning_effort = 'low', service_tier = 'fast', settings_revision = settings_revision + 1
		WHERE id = $1`, conversationID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE codex_thread_controls SET model = 'gpt-5.4',
		reasoning_effort = 'low', service_tier = 'fast', settings_revision = settings_revision + 1
		WHERE discord_conversation_id = $1`, conversationID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_input_messages
		(message_id, conversation_id, discord_user_id, display_name, username,
		 access_snapshot, body) VALUES
		('runtime-message-2',$1,'worker-owner','Owner','owner','owner','second')`, conversationID)
	require.NoError(t, err)
	secondIntent := enqueueWorkerDiscordIntent(t, db, conversationID, "runtime-message-2",
		repositoryID, profileID)
	second, err := client.Claim(ctx, workerprotocol.ClaimRequest{Role: "discord"})
	require.NoError(t, err)
	require.NotNil(t, second.Task)
	require.Equal(t, secondIntent, second.Task.Claimed.ID)
	require.Equal(t, first.Task.Claimed.ControlID, second.Task.Claimed.ControlID)
	require.Equal(t, "gpt-5.4", second.Task.Snapshot.Runtime.Model)
	require.Equal(t, "low", second.Task.Snapshot.Runtime.ReasoningEffort)
	require.Equal(t, "fast", second.Task.Snapshot.Runtime.ServiceTier)
	require.Equal(t, "plan", second.Task.Snapshot.Runtime.CollaborationMode)
}

func TestWorkerAPIDiscordClaimReusesDesktopControl(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	worker, enrollment, err := server.workers.Create(ctx, "desktop-discord-worker", []string{"discord"}, 2)
	require.NoError(t, err)
	_, credential, err := server.workers.Enroll(ctx, enrollment)
	require.NoError(t, err)
	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)
	require.NoError(t, client.Heartbeat(ctx, workerprotocol.HeartbeatRequest{
		WorkerVersion: "test", ProtocolVersion: workerprotocol.Version,
	}))

	repositoryID, _, profileID := seedWorkerGitHubQueue(t, db, 42)
	workspaceID, forumID := seedWorkerWorkspace(t, db, repositoryID, worker.ID)
	projectID := workspaceProjectIDForForum(t, db, forumID)
	var conversationID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_conversations
		(guild_id, forum_id, thread_id, owner_discord_user_id,
		 workspace_project_id, agent_profile_id, title)
		VALUES ('worker-test-guild',$1,'desktop-bound-thread','worker-owner',$2,$3,
		'Desktop bound thread') RETURNING id`, forumID, projectID, profileID).
		Scan(&conversationID))
	var controlID uuid.UUID
	var sessionID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_sessions(
		workspace_id,workspace_project_id,agent_profile_id,title)
		VALUES ($1,$2,$3,'Desktop bound thread') RETURNING id`, workspaceID, projectID,
		profileID).Scan(&sessionID))
	_, err = db.ExecContext(ctx, `UPDATE discord_conversations SET session_id=$2 WHERE id=$1`,
		conversationID, sessionID)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_thread_controls
		(source_type, session_id, discord_conversation_id, workspace_project_id, agent_profile_id,
			worker_id, workspace_id, external_thread_id)
		VALUES ('workspace_session',$1,$2,$3,$4,$5,$6,'codex-desktop-bound-thread')
		RETURNING id`, sessionID, conversationID, projectID, profileID, worker.ID, workspaceID).
		Scan(&controlID))
	_, err = db.ExecContext(ctx, `INSERT INTO discord_input_messages
		(message_id, conversation_id, discord_user_id, display_name, username,
		 access_snapshot, body)
		VALUES ('desktop-bound-followup',$1,'worker-owner','Owner','owner','owner','continue')`,
		conversationID)
	require.NoError(t, err)
	intentID := enqueueWorkerDiscordIntent(t, db, conversationID, "desktop-bound-followup",
		repositoryID, profileID)

	claimed, err := client.Claim(ctx, workerprotocol.ClaimRequest{
		Role: "discord",
	})
	require.NoError(t, err)
	require.NotNil(t, claimed.Task)
	require.Equal(t, intentID, claimed.Task.Claimed.ID)
	require.Equal(t, controlID, claimed.Task.Claimed.ControlID)
	require.Equal(t, codexcontrol.SourceWorkspace, claimed.Task.Claimed.SourceType)
	require.Equal(t, "codex-desktop-bound-thread", claimed.Task.Claimed.ExternalThreadID)
	var controls int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM codex_thread_controls
		WHERE discord_conversation_id=$1`, conversationID).Scan(&controls))
	require.Equal(t, 1, controls)
	require.NoError(t, client.RecordSubmission(ctx, claimed.Task, "discord-active-turn"))
	require.NoError(t, client.ConfirmTurn(ctx, claimed.Task, "discord-active-turn"))
	desktopSteer := workerprotocol.DesktopSteerRecordRequest{
		WorkspaceID: workspaceID, RequestKey: strings.Repeat("a", 64),
		Params: json.RawMessage(`{"threadId":"codex-desktop-bound-thread",` +
			`"expectedTurnId":"discord-active-turn",` +
			`"clientUserMessageId":"desktop-steers-discord-turn",` +
			`"input":[{"type":"text","text":"desktop joins the discord turn"}]}`),
	}
	require.NoError(t, client.RecordDesktopSteer(ctx, desktopSteer))
	require.NoError(t, client.RecordDesktopSteer(ctx, desktopSteer),
		"Desktop 重试记录同一条 Steer 时必须幂等")
	var desktopSteerSurface, desktopSteerStatus, desktopSteerAction, desktopSteerText string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT input_surface, status,
		resolved_action, instruction FROM codex_turn_intents
		WHERE idempotency_key=$1`, "desktop-steer:"+workspaceID.String()+":"+
		strings.Repeat("a", 64)).Scan(&desktopSteerSurface, &desktopSteerStatus,
		&desktopSteerAction, &desktopSteerText))
	require.Equal(t, "desktop", desktopSteerSurface)
	require.Equal(t, "running", desktopSteerStatus)
	require.Equal(t, "steer", desktopSteerAction)
	require.Equal(t, "desktop joins the discord turn", desktopSteerText)
	heartbeat, err := client.RunHeartbeat(ctx, claimed.Task)
	require.NoError(t, err)
	require.Empty(t, heartbeat.Commands,
		"Desktop 已直接提交给同一 App Server 的 Steer 不得再回送给 Worker")
	require.NoError(t, client.Complete(ctx, claimed.Task, codexcontrol.TurnResult{
		TurnID: "discord-active-turn", FinalAnswer: "completed from shared thread",
	}))
	desktopTurn, err := client.PrepareDesktopTurn(ctx, workerprotocol.DesktopTurnPrepareRequest{
		WorkspaceID: workspaceID, RequestKey: strings.Repeat("b", 64), Params: json.RawMessage(
			`{"threadId":"codex-desktop-bound-thread","clientUserMessageId":"desktop-next-turn",` +
				`"input":[{"type":"text","text":"desktop starts the next turn"}]}`),
	})
	require.NoError(t, err,
		"Discord 创建的 Thread 首轮结束后，Desktop 应能在没有 desktop_thread_request 的情况下发起新 Turn")
	require.Equal(t, controlID, desktopTurn.Claimed.ControlID)
	require.Equal(t, "desktop", desktopTurn.Claimed.InputSurface)
	require.Equal(t, conversationID, desktopTurn.Claimed.DiscordConversationID)
	require.NotNil(t, desktopTurn.Snapshot.Discord)
	require.Equal(t, "desktop starts the next turn", desktopTurn.Snapshot.Discord.Body)
	require.Equal(t, forumID, desktopTurn.Snapshot.Discord.ForumID)
	require.Equal(t, workspaceID, desktopTurn.Snapshot.Discord.WorkspaceID)
	require.NoError(t, client.Complete(ctx, &desktopTurn, codexcontrol.TurnResult{
		TurnID: "desktop-next-turn", FinalAnswer: "completed from desktop",
	}))
}

func TestWorkerAPIDesktopThreadEventuallyBindsDiscordPost(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	media := &fakeDesktopImageRemote{}
	server.desktopImageRemote = func(context.Context) (desktopImageDiscord, error) {
		return media, nil
	}
	worker, enrollment, err := server.workers.Create(ctx, "desktop-worker", []string{"discord"}, 2)
	require.NoError(t, err)
	_, credential, err := server.workers.Enroll(ctx, enrollment)
	require.NoError(t, err)
	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)
	repositoryID, _, _ := seedWorkerGitHubQueue(t, db, 41)
	workspaceID, forumID := seedWorkerWorkspace(t, db, repositoryID, worker.ID)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_members
		(guild_id, discord_user_id, username, display_name)
		VALUES ('worker-test-guild','desktop-user','desktop','Desktop Alice')`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE worker_workspaces
		SET owner_discord_user_id='desktop-user' WHERE id=$1`, workspaceID)
	require.NoError(t, err)
	manifest, err := client.Workspace(ctx)
	require.NoError(t, err)
	require.NotNil(t, manifest)
	require.NotNil(t, manifest.OwnerParticipant)
	require.Equal(t, "desktop-user", manifest.OwnerParticipant.DiscordUserID)
	require.Equal(t, "Desktop Alice", manifest.OwnerParticipant.DisplayName)
	require.Equal(t, participantidentity.ID("worker-test-guild", "desktop-user"),
		manifest.OwnerParticipant.ParticipantID)
	var workspaceRelative string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT project.relative_path
		FROM discord_forums forum JOIN workspace_projects project
		ON project.id=forum.workspace_project_id WHERE forum.id=$1`, forumID).
		Scan(&workspaceRelative))
	var repositoryName string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT name FROM repositories WHERE id = $1`,
		repositoryID).Scan(&repositoryName))
	require.Equal(t, "workspaces/"+repositoryName, workspaceRelative)
	workspace := "/var/lib/tyrs-hand/" + workspaceRelative

	state, err := client.PrepareDesktopThread(ctx, workerprotocol.DesktopThreadPrepareRequest{
		WorkspaceID: workspaceID, Operation: "start", RequestKey: strings.Repeat("a", 64),
		Params: json.RawMessage(`{"cwd":"` + workspace + `/nested","model":"mock-model","effort":"high"}`),
	})
	if err != nil {
		var requestID uuid.UUID
		require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM desktop_thread_requests
			WHERE workspace_id = $1 AND request_key = $2`, workspaceID, strings.Repeat("a", 64)).Scan(&requestID))
		testContext, _ := gin.CreateTestContext(httptest.NewRecorder())
		testContext.Request = httptest.NewRequest("GET", "/", nil)
		testContext.Set(workerContextKey, worker)
		_, directErr := server.loadDesktopThreadState(testContext, requestID)
		require.NoError(t, directErr)
	}
	require.NoError(t, err)
	require.Equal(t, "preparing", state.Status)
	var controls int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM codex_thread_controls`).Scan(&controls))
	require.Zero(t, controls)

	response := json.RawMessage(`{"thread":{"id":"codex-desktop-thread"},` +
		`"model":"mock-model","reasoningEffort":"high","serviceTier":"standard"}`)
	state, err = client.CompleteDesktopThread(ctx, state.ID,
		workerprotocol.DesktopThreadCompleteRequest{WorkspaceID: workspaceID, Response: response})
	require.NoError(t, err)
	require.Equal(t, "waiting_for_input", state.Status)
	require.NotEqual(t, uuid.Nil, state.ControlID)
	require.Equal(t, "mock-model", state.Config.Model)
	require.Equal(t, "high", state.Config.ReasoningEffort)
	require.Equal(t, "codex-desktop-thread", state.ExternalThreadID)
	var boundEnvironment uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT workspace_id
		FROM codex_thread_controls WHERE id = $1`, state.ControlID).Scan(&boundEnvironment))
	require.Equal(t, workspaceID, boundEnvironment)
	require.NoError(t, client.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
		WorkspaceID: workspaceID, Generation: 10,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 1, Kind: "name",
			Name: "首条输入前的正式标题",
		}},
	}))

	fork, err := client.PrepareDesktopThread(ctx, workerprotocol.DesktopThreadPrepareRequest{
		WorkspaceID: workspaceID, Operation: "fork", RequestKey: strings.Repeat("b", 64),
		Params: json.RawMessage(`{"threadId":"codex-desktop-thread"}`),
	})
	require.NoError(t, err, "Fork 不应依赖源 Thread 已经创建 Discord Conversation")
	require.Equal(t, "preparing", fork.Status)
	fork, err = client.CompleteDesktopThread(ctx, fork.ID,
		workerprotocol.DesktopThreadCompleteRequest{WorkspaceID: workspaceID,
			Response: json.RawMessage(`{"thread":{"id":"codex-desktop-fork"},` +
				`"model":"mock-model","reasoningEffort":"high","serviceTier":"standard"}`)})
	require.NoError(t, err)
	require.Equal(t, "waiting_for_input", fork.Status)
	require.Equal(t, state.Config, fork.Config)
	require.NoError(t, client.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
		WorkspaceID: workspaceID, Generation: 12,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 1, Kind: "settings", Source: "desktop",
			Model: "gpt-5.6-sol", ReasoningEffort: "ultra", ServiceTier: "priority",
			CollaborationMode: "plan",
		}},
	}))
	state, err = client.DesktopThreadState(ctx, state.ID)
	require.NoError(t, err)
	require.Equal(t, "gpt-5.6-sol", state.Config.Model)
	require.Equal(t, "ultra", state.Config.ReasoningEffort)
	require.Equal(t, "fast", state.Config.ServiceTier)
	require.NoError(t, client.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
		WorkspaceID: workspaceID, Generation: 12,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 2, Kind: "settings", Source: "desktop",
			CollaborationMode: "default",
		}},
	}))
	require.NoError(t, client.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
		WorkspaceID: workspaceID, Generation: 12,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 1, Kind: "settings", Source: "desktop",
			CollaborationMode: "plan",
		}},
	}))
	var metadataMode string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT collaboration_mode
		FROM codex_thread_controls WHERE id = $1`, state.ControlID).Scan(&metadataMode))
	require.Equal(t, "default", metadataMode, "乱序 settings 事件不能覆盖较新模式")
	require.NoError(t, client.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
		WorkspaceID: workspaceID, Generation: 12,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 3, Kind: "settings", Source: "desktop",
			CollaborationMode: "plan",
		}},
	}))
	require.NoError(t, client.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
		WorkspaceID: workspaceID, Generation: 12,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 4, Kind: "settings", Source: "app_server",
			Model: "gpt-5.6-terra", ReasoningEffort: "low", ServiceTier: "fast",
			CollaborationMode: "default", SettingsRevision: 2,
		}},
	}))
	var desiredModel, desiredMode, desiredTier, appliedModel, appliedMode, appliedTier string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COALESCE(model,''), collaboration_mode,
		COALESCE(service_tier,''), COALESCE(applied_model,''),
		COALESCE(applied_collaboration_mode,''), COALESCE(applied_service_tier,'')
		FROM codex_thread_controls WHERE id = $1`, state.ControlID).
		Scan(&desiredModel, &desiredMode, &desiredTier, &appliedModel, &appliedMode, &appliedTier))
	require.Equal(t, "gpt-5.6-sol", desiredModel)
	require.Equal(t, "plan", desiredMode)
	require.Equal(t, "fast", desiredTier)
	require.Equal(t, "gpt-5.6-terra", appliedModel)
	require.Equal(t, "default", appliedMode)
	require.Equal(t, "priority", appliedTier)

	imageContent := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 128)...)
	imagePath := filepath.Join(t.TempDir(), "desktop-shot.png")
	require.NoError(t, os.WriteFile(imagePath, imageContent, 0o600))
	imageDigest := fmt.Sprintf("%x", sha256.Sum256(imageContent))
	task, err := client.PrepareDesktopTurn(ctx, workerprotocol.DesktopTurnPrepareRequest{
		WorkspaceID: workspaceID, RequestKey: strings.Repeat("d", 64), Params: json.RawMessage(
			`{"threadId":"codex-desktop-thread","clientUserMessageId":"desktop-client-message-1",` +
				`"collaborationMode":{"mode":"plan","settings":{"model":"gpt-5.6-sol"}},` +
				`"input":[{"type":"text","text":"<codex_delegation>\n` +
				`<source_thread_id>source-thread</source_thread_id>\n` +
				`<input>desktop asks &amp;&amp; checks ` + imagePath +
				`</input>\n</codex_delegation>"},{"type":"localImage","path":"` + imagePath + `"}]}`),
		Images: []workerprotocol.DesktopImage{{Filename: filepath.Base(imagePath),
			MediaType: "image/png", Size: int64(len(imageContent)), SHA256: imageDigest}},
	})
	require.NoError(t, err)
	require.Equal(t, "desktop", task.Claimed.InputSurface)
	require.Empty(t, task.Claimed.DiscordMessageID)
	require.NotNil(t, task.Snapshot.Session)
	require.Equal(t, "gpt-5.6-sol", task.Snapshot.Runtime.Model)
	require.Equal(t, "ultra", task.Snapshot.Runtime.ReasoningEffort)
	require.Equal(t, "fast", task.Snapshot.Runtime.ServiceTier)
	require.Equal(t, "plan", task.Snapshot.Runtime.CollaborationMode)
	require.Equal(t, "desktop asks && checks "+imagePath, task.Snapshot.Session.Body)
	require.Equal(t, "Desktop Alice", task.Snapshot.Session.DisplayName)
	require.Equal(t, participantidentity.ID("worker-test-guild", "desktop-user"),
		task.Claimed.ActorParticipantID)
	require.Equal(t, "Desktop Alice", task.Claimed.ActorDisplayName)
	target, err := client.DesktopImageTarget(ctx, task.Claimed.ID)
	require.NoError(t, err)
	require.Equal(t, "waiting", target.Status)
	outbox := discordintegration.NewSQLoutbox(db)
	item, err := outbox.Claim(ctx, time.Minute)
	require.NoError(t, err)
	require.NotNil(t, item)
	require.Equal(t, "desktop-thread-post:"+state.ID.String(), item.OperationKey)
	require.Contains(t, string(item.Payload), "Desktop Alice")
	require.Contains(t, string(item.Payload), "desktop asks && checks")
	require.Contains(t, string(item.Payload), filepath.Base(imagePath))
	require.NotContains(t, string(item.Payload), imagePath)
	require.NotContains(t, string(item.Payload), "codex_delegation")
	require.NotContains(t, string(item.Payload), "source_thread_id")
	require.Contains(t, string(item.Payload), "首条输入前的正式标题")
	completeWorkerOutbox(t, ctx, outbox, item,
		json.RawMessage(`{"threadId":"desktop-discord-thread","messageId":"desktop-starter"}`))
	target, err = client.DesktopImageTarget(ctx, task.Claimed.ID)
	require.NoError(t, err)
	require.Equal(t, "ready", target.Status)
	uploaded, err := client.UploadDesktopImage(ctx, task.Claimed.ID, 0,
		workerprotocol.DesktopImage{Filename: filepath.Base(imagePath), SourcePath: imagePath}, false)
	require.NoError(t, err)
	require.Equal(t, "delivered", uploaded.Status)
	require.Equal(t, imageContent, media.content)
	require.Len(t, media.card.Media, 1)
	var imageStatus, attachmentID string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status,
		COALESCE(discord_attachment_id,'') FROM desktop_turn_images
		WHERE intent_id=$1 AND ordinal=0`, task.Claimed.ID).Scan(&imageStatus, &attachmentID))
	require.Equal(t, "delivered", imageStatus)
	require.Equal(t, "desktop-attachment", attachmentID)
	state, err = client.DesktopThreadState(ctx, state.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", state.Status)
	require.NotEqual(t, uuid.Nil, state.ConversationID)
	var conversationControlID uuid.UUID
	var titleRenameStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT control.id, conversation.title_rename_status
		FROM discord_conversations conversation JOIN codex_thread_controls control
			ON control.discord_conversation_id=conversation.id WHERE conversation.id=$1`, state.ConversationID).
		Scan(&conversationControlID, &titleRenameStatus))
	require.Equal(t, state.ControlID, conversationControlID)
	require.Equal(t, "skipped", titleRenameStatus,
		"Desktop 投影必须由 Codex 标题链路负责，不能进入 Luna 标题队列")
	require.NoError(t, client.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
		WorkspaceID: workspaceID, Generation: 12,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 5, Kind: "settings", Source: "desktop",
			CollaborationMode: "default",
		}},
	}))
	var controlMode, discordMode string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT control.collaboration_mode,
		conversation.collaboration_mode FROM codex_thread_controls control
		JOIN discord_conversations conversation ON conversation.id=control.discord_conversation_id
		WHERE control.id=$1`, state.ControlID).Scan(&controlMode, &discordMode))
	require.Equal(t, "default", controlMode)
	require.Equal(t, "default", discordMode,
		"Desktop 模式需要同步到关联的 Discord 会话")
	var memberAddPayload string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT payload::text FROM integration_outbox
		WHERE operation_key=$1`, "desktop-thread-member:"+state.ID.String()).
		Scan(&memberAddPayload))
	require.Contains(t, memberAddPayload, `"userId": "desktop-user"`)
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
		WorkspaceID: workspaceID, Generation: 10,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 2, Kind: "name",
			Name: "Atlas 正式标题",
		}},
	}))
	require.NoError(t, client.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
		WorkspaceID: workspaceID, Generation: 10,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 1, Kind: "name",
			Name: "迟到的旧标题",
		}},
	}))
	var desiredName, conversationTitle string
	var desiredRevision int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT ct.desired_thread_name,
		ct.desired_thread_name_revision, c.title FROM codex_thread_controls ct
		JOIN discord_conversations c ON c.id = ct.discord_conversation_id
		WHERE ct.id = $1`, state.ControlID).
		Scan(&desiredName, &desiredRevision, &conversationTitle))
	require.Equal(t, "Atlas 正式标题", desiredName)
	require.Equal(t, int64(2), desiredRevision)
	require.Equal(t, desiredName, conversationTitle)
	var renamePayload string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT payload::text FROM integration_outbox
		WHERE operation_key = $1`, "thread-name:"+state.ControlID.String()).Scan(&renamePayload))
	require.Contains(t, renamePayload, "Atlas 正式标题")
	require.Contains(t, renamePayload, `"revision": 2`)
	var renameItem *discordintegration.OutboxItem
	for {
		renameItem, err = outbox.Claim(ctx, time.Minute)
		require.NoError(t, err)
		require.NotNil(t, renameItem)
		if renameItem.OperationKey == "thread-name:"+state.ControlID.String() {
			break
		}
		require.NoError(t, outbox.RetryDelivery(ctx, *renameItem, time.Now().Add(time.Hour),
			errors.New("推迟无关投影")))
	}
	require.NoError(t, client.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
		WorkspaceID: workspaceID, Generation: 10,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 3, Kind: "name",
			Name: "竞争后的最新标题",
		}},
	}))
	completeWorkerOutbox(t, ctx, outbox, renameItem, json.RawMessage(`{}`))
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
	completeWorkerOutbox(t, ctx, outbox, renameItem, json.RawMessage(`{}`))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COALESCE(applied_thread_name,''),
		applied_thread_name_revision FROM codex_thread_controls WHERE id=$1`,
		state.ControlID).Scan(&appliedName, &appliedRevision))
	require.Equal(t, desiredName, appliedName)
	require.Equal(t, desiredRevision, appliedRevision)
	require.NoError(t, client.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
		WorkspaceID: workspaceID, Generation: 10,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 4, Kind: "name",
			Name: "即将失败的旧标题",
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
		WorkspaceID: workspaceID, Generation: 10,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 5, Kind: "name",
			Name: "失败竞争后的最新标题",
		}},
	}))
	require.NoError(t, outbox.FailDelivery(ctx, *renameItem, errors.New("旧 rename 失败")))
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
	require.NoError(t, discordintegration.NewConversationService(db).Reply(ctx,
		discordintegration.IncomingMessage{
			GuildID: "worker-test-guild", ThreadID: "desktop-discord-thread",
			MessageID: "discord-steer-desktop-1", DiscordUserID: "desktop-user",
			DisplayName: "Desktop Alice", Username: "desktop",
			Body: "discord follows up while desktop is running",
		}))
	heartbeat, err := client.RunHeartbeat(ctx, &task)
	require.NoError(t, err)
	require.Len(t, heartbeat.Commands, 1)
	require.NotNil(t, heartbeat.Commands[0].Discord)
	require.Equal(t, "discord follows up while desktop is running",
		heartbeat.Commands[0].Instruction)
	require.Equal(t, "discord-steer-desktop-1", heartbeat.Commands[0].Discord.MessageID)
	require.Equal(t, "discord follows up while desktop is running",
		heartbeat.Commands[0].Discord.Body)
	require.NoError(t, client.AckCommand(ctx, &task, heartbeat.Commands[0],
		"steer", "desktop-turn-1"))
	_, err = db.ExecContext(ctx, `INSERT INTO discord_members
		(guild_id, discord_user_id, username, display_name)
		VALUES ('worker-test-guild','discord-operator','operator','Discord Operator')`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_forum_access
		(forum_id, discord_user_id, access_level)
		VALUES ($1,'discord-operator','operator')`, forumID)
	require.NoError(t, err)
	conversationService := discordintegration.NewConversationService(db)
	require.NoError(t, conversationService.Reply(ctx, discordintegration.IncomingMessage{
		GuildID: "worker-test-guild", ThreadID: "desktop-discord-thread",
		MessageID: "discord-steer-desktop-2", DiscordUserID: "discord-operator",
		DisplayName: "Discord Operator", Username: "operator",
		Body: "operator adds a file from discord",
		Attachments: []discordintegration.IncomingAttachment{{
			ID: "discord-attachment-1", URL: "https://example.invalid/context.txt",
			Filename: "context.txt", MediaType: "text/plain", Size: 12,
			Kind: "file", SHA256: strings.Repeat("a", 64),
			StorageKey: "discord/discord-steer-desktop-2/context.txt",
		}},
	}))
	heartbeat, err = client.RunHeartbeat(ctx, &task)
	require.NoError(t, err)
	require.Len(t, heartbeat.Commands, 1)
	operatorCommand := heartbeat.Commands[0]
	require.NotNil(t, operatorCommand.Discord)
	require.Equal(t, "discord-steer-desktop-2", operatorCommand.Discord.MessageID)
	require.Equal(t, "operator adds a file from discord", operatorCommand.Discord.Body)
	require.Equal(t, "discord-operator", operatorCommand.Discord.UserID)
	require.Equal(t, "Discord Operator", operatorCommand.Discord.DisplayName)
	require.Equal(t, "operator", operatorCommand.Discord.Access)
	require.NotNil(t, operatorCommand.Discord.Project)
	require.Equal(t, state.ConversationID,
		operatorCommand.Discord.Project.ConversationID)
	require.Len(t, operatorCommand.Discord.Attachments, 1)
	require.Equal(t, "context.txt", operatorCommand.Discord.Attachments[0].Filename)
	require.Equal(t, strings.Repeat("a", 64), operatorCommand.Discord.Attachments[0].SHA256)
	repeatedHeartbeat, err := client.RunHeartbeat(ctx, &task)
	require.NoError(t, err)
	require.Len(t, repeatedHeartbeat.Commands, 1)
	require.Equal(t, operatorCommand.ID, repeatedHeartbeat.Commands[0].ID)
	require.Equal(t, operatorCommand.Discord.MessageID,
		repeatedHeartbeat.Commands[0].Discord.MessageID)
	require.NoError(t, client.AckCommand(ctx, &task, operatorCommand,
		"steer", "desktop-turn-1"))

	for _, message := range []struct{ id, body string }{
		{"discord-steer-desktop-3", "third input from discord"},
		{"discord-steer-desktop-4", "fourth input from discord"},
	} {
		require.NoError(t, conversationService.Reply(ctx, discordintegration.IncomingMessage{
			GuildID: "worker-test-guild", ThreadID: "desktop-discord-thread",
			MessageID: message.id, DiscordUserID: "desktop-user",
			DisplayName: "Desktop Alice", Username: "desktop", Body: message.body,
		}))
	}
	heartbeat, err = client.RunHeartbeat(ctx, &task)
	require.NoError(t, err)
	require.Len(t, heartbeat.Commands, 2)
	require.Equal(t, "discord-steer-desktop-3", heartbeat.Commands[0].Discord.MessageID)
	require.Equal(t, "third input from discord", heartbeat.Commands[0].Discord.Body)
	require.Equal(t, "discord-steer-desktop-4", heartbeat.Commands[1].Discord.MessageID)
	require.Equal(t, "fourth input from discord", heartbeat.Commands[1].Discord.Body)
	for _, command := range heartbeat.Commands {
		require.NoError(t, client.AckCommand(ctx, &task, command,
			"steer", "desktop-turn-1"))
	}
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
		":message:"+task.Claimed.ProjectionAnchor).Scan(&timelineProjection))
	require.Equal(t, 1, timelineProjection,
		"Desktop timeline-only 事件必须复用该 Intent 的投影锚点")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM discord_projections
		WHERE projection_key = $1`, "conversation:"+state.ConversationID.String()+
		":message:").Scan(&emptyAnchorProjection))
	require.Zero(t, emptyAnchorProjection, "Desktop timeline-only 事件不得创建空锚点 Projection")
	steerRequest := workerprotocol.DesktopSteerRecordRequest{
		WorkspaceID: workspaceID, RequestKey: strings.Repeat("f", 64),
		Params: json.RawMessage(`{"threadId":"codex-desktop-thread",` +
			`"expectedTurnId":"desktop-turn-1",` +
			`"clientUserMessageId":"desktop-client-steer-1",` +
			`"input":[{"type":"text","text":"<codex_delegation>` +
			`<source_thread_id>source-thread</source_thread_id>` +
			`<input>desktop follows up</input></codex_delegation>"}]}`),
	}
	require.NoError(t, client.RecordDesktopSteer(ctx, steerRequest))
	require.NoError(t, client.RecordDesktopSteer(ctx, steerRequest),
		"Desktop Steer 重试不得重复创建 Intent")
	var steerInputProjection int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM integration_outbox
		WHERE operation_key LIKE $1`, "projection:desktop-input:"+state.ConversationID.String()+":"+
		"desktop-client-steer-1:%").Scan(&steerInputProjection))
	require.Equal(t, 1, steerInputProjection)
	var steerPayload string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT payload::text FROM integration_outbox
		WHERE operation_key LIKE $1`, "projection:desktop-input:"+state.ConversationID.String()+":"+
		"desktop-client-steer-1:%").Scan(&steerPayload))
	require.Contains(t, steerPayload, "desktop follows up")
	require.NotContains(t, steerPayload, "codex_delegation")
	desktopSteerStatusKey := "conversation:" + state.ConversationID.String() +
		":message:desktop-client-steer-1"
	var desktopSteerCardRole string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT role FROM discord_turn_status_cards
		WHERE run_id=$1 AND projection_key=$2`, task.Claimed.RunID, desktopSteerStatusKey).
		Scan(&desktopSteerCardRole))
	require.Equal(t, "pending", desktopSteerCardRole,
		"Desktop Steer 的新过程卡应等待 Discord 创建消息后迁移")
	var desktopSteerStatusOutbox *discordintegration.OutboxItem
	for attempt := 0; attempt < 100; attempt++ {
		candidate, claimErr := outbox.Claim(ctx, time.Minute)
		require.NoError(t, claimErr)
		require.NotNil(t, candidate)
		if candidate.OperationKey == "projection:"+desktopSteerStatusKey {
			desktopSteerStatusOutbox = candidate
			break
		}
		require.NoError(t, outbox.RetryDelivery(ctx, *candidate, time.Now().Add(time.Hour),
			errors.New("推迟无关投影")))
	}
	require.NotNil(t, desktopSteerStatusOutbox)
	completeWorkerOutbox(t, ctx, outbox, desktopSteerStatusOutbox,
		json.RawMessage(`{"threadId":"desktop-discord-thread",`+
			`"messageId":"desktop-steer-status-card"}`))
	var initialCardRole, initialCardHeader, initialCardURL string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT card.role,
		projection.desired_payload->'card'->>'header',
		COALESCE(projection.desired_payload->'card'->'buttons'->0->>'url','')
		FROM discord_turn_status_cards card JOIN discord_projections projection
		ON projection.guild_id=card.guild_id AND projection.projection_key=card.projection_key
		WHERE card.run_id=$1 AND card.projection_key=$2`, task.Claimed.RunID,
		"conversation:"+state.ConversationID.String()+":message:"+task.Claimed.ProjectionAnchor).
		Scan(&initialCardRole, &initialCardHeader, &initialCardURL))
	require.Equal(t, "history", initialCardRole)
	require.Equal(t, "Codex · 已引导对话", initialCardHeader)
	require.Empty(t, initialCardURL)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT role FROM discord_turn_status_cards
		WHERE run_id=$1 AND projection_key=$2`, task.Claimed.RunID, desktopSteerStatusKey).
		Scan(&desktopSteerCardRole))
	require.Equal(t, "current", desktopSteerCardRole,
		"Desktop Steer 过程卡投影完成后应成为当前过程卡")
	var steerIntentID, steerParticipantID uuid.UUID
	var steerStatus, steerDisplayName, steerProjectionStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id, status, actor_participant_id,
		actor_display_name, desktop_input_projection_status
		FROM codex_turn_intents WHERE idempotency_key=$1`,
		"desktop-steer:"+workspaceID.String()+":"+strings.Repeat("f", 64)).
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
	_, err = client.RunHeartbeat(ctx, &task)
	require.NoError(t, err, "等待用户回答时必须保留 Run 租约以接收停止和 steer 指令")
	answered, err := client.AnswerInteractive(ctx, workerprotocol.InteractiveAnswerRequest{
		WorkspaceID: workspaceID, ThreadID: "codex-desktop-thread", TurnID: "desktop-turn-1",
		ItemID: "question-1", Surface: "discord",
		Answer: json.RawMessage(`{"answers":{"choice":{"answers":["Yes"]}}}`),
	})
	require.NoError(t, err)
	require.True(t, answered.Accepted)
	require.True(t, answered.Ready, "回答获胜后应在有空闲槽时恢复运行")
	duplicate, err := client.AnswerInteractive(ctx, workerprotocol.InteractiveAnswerRequest{
		WorkspaceID: workspaceID, ThreadID: "codex-desktop-thread", TurnID: "desktop-turn-1",
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
		WorkspaceID: workspaceID, ThreadID: "codex-desktop-thread", TurnID: "desktop-turn-1",
		ItemID: "question-secret", Surface: "discord", Answer: secretAnswer,
	})
	require.Error(t, err, "Secret 回答不得从 Discord 提交")
	secretState, err := client.AnswerInteractive(ctx, workerprotocol.InteractiveAnswerRequest{
		WorkspaceID: workspaceID, ThreadID: "codex-desktop-thread", TurnID: "desktop-turn-1",
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
		WHERE operation_key = $1`, "projection:conversation-reply:"+state.ConversationID.String()+
		":message:"+task.Claimed.ProjectionAnchor).Scan(&projectedReply))
	require.Equal(t, 1, projectedReply)
	rollback, err := client.PrepareDesktopRollback(ctx,
		workerprotocol.DesktopRollbackPrepareRequest{
			WorkspaceID: workspaceID, RequestKey: strings.Repeat("b", 64),
			Params: json.RawMessage(`{"threadId":"codex-desktop-thread","numTurns":1}`),
		})
	require.NoError(t, err)
	require.Equal(t, "reserved", rollback.Status)
	retriedRollback, err := client.PrepareDesktopRollback(ctx,
		workerprotocol.DesktopRollbackPrepareRequest{
			WorkspaceID: workspaceID, RequestKey: strings.Repeat("b", 64),
			Params: json.RawMessage(`{"threadId":"codex-desktop-thread","numTurns":1}`),
		})
	require.NoError(t, err)
	require.Equal(t, rollback.ID, retriedRollback.ID, "同一目标的重试必须保持幂等")
	require.NoError(t, client.CompleteDesktopRollback(ctx, rollback.ID,
		workerprotocol.DesktopRollbackCompleteRequest{WorkspaceID: workspaceID,
			Response: json.RawMessage(`{}`)}))
	preflight, err := client.PreflightDesktopTurn(ctx, workerprotocol.DesktopTurnPreflightRequest{
		WorkspaceID: workspaceID,
		Params: json.RawMessage(`{"threadId":"codex-desktop-thread",` +
			`"clientUserMessageId":"desktop-edited",` +
			`"input":[{"type":"text","text":"desktop edited"}]}`),
	})
	require.NoError(t, err)
	require.Contains(t, string(preflight.Params), rollback.ID.String())
	replacementTask, err := client.PrepareDesktopTurn(ctx,
		workerprotocol.DesktopTurnPrepareRequest{WorkspaceID: workspaceID,
			RequestKey: strings.Repeat("c", 64), Params: preflight.Params})
	require.NoError(t, err)
	require.Equal(t, rollback.ID, replacementTask.Claimed.ID)
	require.Equal(t, "replace_last_turn", replacementTask.Claimed.Operation)
	require.Equal(t, "desktop-client-steer-1", replacementTask.Claimed.ProjectionAnchor)
	require.NoError(t, client.RecordSubmission(ctx, &replacementTask, "desktop-turn-replacement"))
	require.NoError(t, client.ConfirmTurn(ctx, &replacementTask, "desktop-turn-replacement"))
	require.NoError(t, client.Complete(ctx, &replacementTask, codexcontrol.TurnResult{
		TurnID: "desktop-turn-replacement", FinalAnswer: "desktop edited done",
	}))
	nextTask, err := client.PrepareDesktopTurn(ctx, workerprotocol.DesktopTurnPrepareRequest{
		WorkspaceID: workspaceID, RequestKey: strings.Repeat("0", 64),
		Params: json.RawMessage(`{"threadId":"codex-desktop-thread",` +
			`"clientUserMessageId":"desktop-next",` +
			`"input":[{"type":"text","text":"next turn"}]}`),
	})
	require.NoError(t, err)
	require.NoError(t, client.RecordSubmission(ctx, &nextTask, "desktop-turn-next"))
	require.NoError(t, client.ConfirmTurn(ctx, &nextTask, "desktop-turn-next"))
	require.NoError(t, client.Complete(ctx, &nextTask, codexcontrol.TurnResult{
		TurnID: "desktop-turn-next", FinalAnswer: "next done",
	}))
	secondRollback, err := client.PrepareDesktopRollback(ctx,
		workerprotocol.DesktopRollbackPrepareRequest{
			WorkspaceID: workspaceID, RequestKey: strings.Repeat("b", 64),
			Params: json.RawMessage(`{"threadId":"codex-desktop-thread","numTurns":1}`),
		})
	require.NoError(t, err, "同一 Thread 的后续 turn 必须允许再次 rollback")
	require.NotEqual(t, rollback.ID, secondRollback.ID)
	require.NoError(t, client.CompleteDesktopRollback(ctx, secondRollback.ID,
		workerprotocol.DesktopRollbackCompleteRequest{WorkspaceID: workspaceID,
			Error: "test cleanup"}))

	cancelableArchive, err := client.PrepareDesktopThreadLifecycle(ctx,
		workerprotocol.ThreadLifecyclePrepareRequest{
			WorkspaceID: workspaceID, ThreadID: "codex-desktop-thread",
			DesiredState: "archived",
		})
	require.NoError(t, err)
	require.Equal(t, "waiting_for_turn", cancelableArchive.Status)
	canceledByUnarchive, err := client.PrepareDesktopThreadLifecycle(ctx,
		workerprotocol.ThreadLifecyclePrepareRequest{
			WorkspaceID: workspaceID, ThreadID: "codex-desktop-thread",
			DesiredState: "active",
		})
	require.NoError(t, err)
	require.Equal(t, "completed", canceledByUnarchive.Status)
	var canceledDesktopArchive string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status
		FROM codex_thread_lifecycle_requests WHERE id=$1`, cancelableArchive.ID).
		Scan(&canceledDesktopArchive))
	require.Equal(t, "canceled", canceledDesktopArchive)

	archive, err := client.PrepareDesktopThreadLifecycle(ctx,
		workerprotocol.ThreadLifecyclePrepareRequest{
			WorkspaceID: workspaceID, ThreadID: "codex-desktop-thread",
			DesiredState: "archived",
		})
	require.NoError(t, err)
	require.Equal(t, "waiting_for_turn", archive.Status)
	archiveReady, err := client.ThreadLifecycleState(ctx, archive.ID)
	require.NoError(t, err)
	require.Equal(t, "applying", archiveReady.Status,
		"Control Run 终态后才允许 AppServer Hub 调用官方 archive")
	_, err = client.PrepareDesktopTurn(ctx, workerprotocol.DesktopTurnPrepareRequest{
		WorkspaceID: workspaceID, RequestKey: strings.Repeat("6", 64), Params: json.RawMessage(
			`{"threadId":"codex-desktop-thread","input":[{"type":"text","text":"blocked"}]}`),
	})
	require.Error(t, err, "归档 pending 起必须拒绝新的 Desktop Turn")
	require.NoError(t, client.CompleteThreadLifecycle(ctx, archive.ID,
		workerprotocol.ThreadLifecycleCompleteRequest{
			WorkspaceID: workspaceID, Response: json.RawMessage(`{}`),
		}))
	var lifecycleState string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lifecycle_state
		FROM discord_conversations WHERE id=$1`, state.ConversationID).Scan(&lifecycleState))
	require.Equal(t, "archive_pending", lifecycleState,
		"官方 RPC 返回不能替代 thread/archived 生命周期通知")
	require.NoError(t, client.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
		WorkspaceID: workspaceID, Generation: 10,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 1, Kind: "lifecycle",
			LifecycleState: "archived",
		}},
	}))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lifecycle_state
		FROM discord_conversations WHERE id=$1`, state.ConversationID).Scan(&lifecycleState))
	require.Equal(t, "archived", lifecycleState)
	var clientLifecycleUpdates int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM client_updates update_event
		JOIN codex_thread_controls control ON control.session_id=update_event.session_id
		WHERE control.id=$1 AND update_event.update_type='session.lifecycle'
			AND update_event.payload->>'lifecycleState'='archived'`, archive.ControlID).
		Scan(&clientLifecycleUpdates))
	require.Equal(t, 1, clientLifecycleUpdates,
		"最终 lifecycle 必须作为 durable 事件通知原生客户端")
	require.NoError(t, client.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
		WorkspaceID: workspaceID, Generation: 10,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 1, Kind: "lifecycle",
			LifecycleState: "active",
		}},
	}))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lifecycle_state
		FROM discord_conversations WHERE id=$1`, state.ConversationID).Scan(&lifecycleState))
	require.Equal(t, "archived", lifecycleState,
		"重复或乱序 lifecycle 通知不得覆盖较新事实")
	unarchive, err := client.PrepareDesktopThreadLifecycle(ctx,
		workerprotocol.ThreadLifecyclePrepareRequest{
			WorkspaceID: workspaceID, ThreadID: "codex-desktop-thread",
			DesiredState: "active",
		})
	require.NoError(t, err)
	require.Equal(t, "applying", unarchive.Status)
	require.NoError(t, client.CompleteThreadLifecycle(ctx, unarchive.ID,
		workerprotocol.ThreadLifecycleCompleteRequest{
			WorkspaceID: workspaceID, Response: json.RawMessage(`{}`),
		}))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lifecycle_state
		FROM discord_conversations WHERE id=$1`, state.ConversationID).Scan(&lifecycleState))
	require.Equal(t, "unarchive_pending", lifecycleState,
		"Discord 必须等 thread/unarchived 通知后再解锁")
	require.NoError(t, client.RecordThreadMetadata(ctx, workerprotocol.ThreadMetadataRequest{
		WorkspaceID: workspaceID, Generation: 10,
		Events: []workerprotocol.ThreadMetadataEvent{{
			ThreadID: "codex-desktop-thread", Sequence: 2, Kind: "lifecycle",
			LifecycleState: "active",
		}},
	}))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lifecycle_state
		FROM discord_conversations WHERE id=$1`, state.ConversationID).Scan(&lifecycleState))
	require.Equal(t, "active", lifecycleState)
	failedArchive, err := client.PrepareDesktopThreadLifecycle(ctx,
		workerprotocol.ThreadLifecyclePrepareRequest{
			WorkspaceID: workspaceID, ThreadID: "codex-desktop-thread",
			DesiredState: "archived",
		})
	require.NoError(t, err)
	require.NoError(t, client.CompleteThreadLifecycle(ctx, failedArchive.ID,
		workerprotocol.ThreadLifecycleCompleteRequest{
			WorkspaceID: workspaceID, Error: "archive unavailable",
		}))
	var lifecycleRequestStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT c.lifecycle_state, r.status
		FROM codex_thread_controls c JOIN codex_thread_lifecycle_requests r
			ON r.control_id=c.id WHERE r.id=$1`, failedArchive.ID).
		Scan(&lifecycleState, &lifecycleRequestStatus))
	require.Equal(t, "active", lifecycleState)
	require.Equal(t, "failed", lifecycleRequestStatus)

	discordArchive, err := discordintegration.NewConversationService(db).Archive(ctx,
		"worker-test-guild", "desktop-discord-thread", "desktop-user")
	require.NoError(t, err)
	require.Equal(t, "applying", discordArchive.Status)
	pendingLifecycles, err := client.PendingThreadLifecycles(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, pendingLifecycles)
	require.Equal(t, discordArchive.ID, pendingLifecycles[0].ID)
	require.Equal(t, "archived", pendingLifecycles[0].DesiredState)
	_, err = db.ExecContext(ctx, `UPDATE codex_thread_lifecycle_requests
		SET source='client' WHERE id=$1`, discordArchive.ID)
	require.NoError(t, err)
	pendingLifecycles, err = client.PendingThreadLifecycles(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, pendingLifecycles, "客户端 lifecycle 也必须交给 Worker 应用")
	require.Equal(t, discordArchive.ID, pendingLifecycles[0].ID)
	require.NoError(t, client.CompleteThreadLifecycle(ctx, discordArchive.ID,
		workerprotocol.ThreadLifecycleCompleteRequest{WorkspaceID: workspaceID,
			Error: "archive test rollback"}))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lifecycle_state
		FROM discord_conversations WHERE id=$1`, state.ConversationID).Scan(&lifecycleState))
	require.Equal(t, "active", lifecycleState)

	forkTask, err := client.PrepareDesktopTurn(ctx, workerprotocol.DesktopTurnPrepareRequest{
		WorkspaceID: workspaceID, RequestKey: strings.Repeat("8", 64), Params: json.RawMessage(
			`{"threadId":"codex-desktop-fork","clientUserMessageId":"desktop-fork-message-1",` +
				`"input":[{"type":"text","text":"fork first input"}]}`),
	})
	require.NoError(t, err)
	require.Equal(t, fork.ControlID, forkTask.Claimed.ControlID)
	require.Equal(t, "mock-model", forkTask.Snapshot.Runtime.Model)
	require.Equal(t, "high", forkTask.Snapshot.Runtime.ReasoningEffort)
	require.Equal(t, "standard", forkTask.Snapshot.Runtime.ServiceTier)
	var forkPost *discordintegration.OutboxItem
	for {
		forkPost, err = outbox.Claim(ctx, time.Minute)
		require.NoError(t, err)
		require.NotNil(t, forkPost)
		if forkPost.OperationKey == "desktop-thread-post:"+fork.ID.String() {
			break
		}
		require.NoError(t, outbox.RetryDelivery(ctx, *forkPost, time.Now().Add(time.Hour),
			errors.New("推迟无关投影")))
	}
	require.Contains(t, string(forkPost.Payload), "Desktop Alice")
	require.Contains(t, string(forkPost.Payload), "fork first input")
	completeWorkerOutbox(t, ctx, outbox, forkPost,
		json.RawMessage(`{"threadId":"desktop-discord-fork","messageId":"desktop-fork-starter"}`))
	fork, err = client.DesktopThreadState(ctx, fork.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", fork.Status)
	require.NotEqual(t, state.ConversationID, fork.ConversationID)
	require.NoError(t, client.RecordSubmission(ctx, &forkTask, "desktop-fork-turn-1"))
	require.NoError(t, client.ConfirmTurn(ctx, &forkTask, "desktop-fork-turn-1"))
	waitingArchive, err := discordintegration.NewConversationService(db).Archive(ctx,
		"worker-test-guild", "desktop-discord-fork", "desktop-user")
	require.NoError(t, err)
	require.Equal(t, "waiting_for_turn", waitingArchive.Status)
	pendingLifecycles, err = client.PendingThreadLifecycles(ctx)
	require.NoError(t, err)
	for _, pending := range pendingLifecycles {
		require.NotEqual(t, waitingArchive.ID, pending.ID,
			"活动 Turn 完成前 Worker 不得取得 Discord archive")
	}
	require.NoError(t, client.Complete(ctx, &forkTask, codexcontrol.TurnResult{
		TurnID: "desktop-fork-turn-1", FinalAnswer: "fork done",
	}))
	pendingLifecycles, err = client.PendingThreadLifecycles(ctx)
	require.NoError(t, err)
	foundWaitingArchive := false
	for _, pending := range pendingLifecycles {
		foundWaitingArchive = foundWaitingArchive || pending.ID == waitingArchive.ID
	}
	require.True(t, foundWaitingArchive)
	require.NoError(t, client.CompleteThreadLifecycle(ctx, waitingArchive.ID,
		workerprotocol.ThreadLifecycleCompleteRequest{WorkspaceID: workspaceID,
			Error: "archive waiting test rollback"}))

	secondTask, err := client.PrepareDesktopTurn(ctx, workerprotocol.DesktopTurnPrepareRequest{
		WorkspaceID: workspaceID, RequestKey: strings.Repeat("e", 64), Params: json.RawMessage(
			`{"threadId":"codex-desktop-thread","input":[{"type":"text","text":"local desktop"}]}`),
	})
	require.NoError(t, err)
	require.Equal(t, participantidentity.ID("worker-test-guild", "desktop-user"),
		secondTask.Claimed.ActorParticipantID)
	require.NoError(t, client.RecordSubmission(ctx, &secondTask, "desktop-turn-2"))
	require.NoError(t, client.ConfirmTurn(ctx, &secondTask, "desktop-turn-2"))
	stopped, err := conversationService.Stop(ctx, "worker-test-guild",
		"desktop-discord-thread", "desktop-user")
	require.NoError(t, err)
	require.EqualValues(t, 1, stopped)
	heartbeat, err = client.RunHeartbeat(ctx, &secondTask)
	require.NoError(t, err)
	require.Len(t, heartbeat.Commands, 1)
	require.Equal(t, "interrupt", heartbeat.Commands[0].Operation)
	require.Nil(t, heartbeat.Commands[0].Discord)
	require.NoError(t, client.AckCommand(ctx, &secondTask, heartbeat.Commands[0],
		"interrupt", "desktop-turn-2"))
	require.NoError(t, client.Fail(ctx, &secondTask, "user_interrupt",
		errors.New("stopped from Discord")))
	var stoppedRunStatus, stoppedIntentStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT r.status, i.status
		FROM codex_turn_runs r JOIN codex_turn_intents i ON i.id=r.primary_intent_id
		WHERE r.id=$1`, secondTask.Claimed.RunID).
		Scan(&stoppedRunStatus, &stoppedIntentStatus))
	require.Equal(t, "canceled", stoppedRunStatus)
	require.Equal(t, "canceled", stoppedIntentStatus)

	failed, err := client.PrepareDesktopThread(ctx, workerprotocol.DesktopThreadPrepareRequest{
		WorkspaceID: workspaceID, Operation: "start", RequestKey: strings.Repeat("c", 64),
		Params: json.RawMessage(`{"cwd":"` + workspace + `"}`),
	})
	require.NoError(t, err)
	failed, err = client.CompleteDesktopThread(ctx, failed.ID,
		workerprotocol.DesktopThreadCompleteRequest{WorkspaceID: workspaceID,
			Response: json.RawMessage(`{"thread":{"id":"codex-desktop-failed"}}`)})
	require.NoError(t, err)
	failedTask, err := client.PrepareDesktopTurn(ctx, workerprotocol.DesktopTurnPrepareRequest{
		WorkspaceID: workspaceID, RequestKey: strings.Repeat("9", 64), Params: json.RawMessage(
			`{"threadId":"codex-desktop-failed","clientUserMessageId":"offline-first",` +
				`"input":[{"type":"text","text":"offline post"}]}`),
	})
	require.NoError(t, err)
	require.NotNil(t, failedTask.Snapshot.Session)
	require.Nil(t, failedTask.Snapshot.Discord)
	for {
		item, err = outbox.Claim(ctx, time.Minute)
		require.NoError(t, err)
		require.NotNil(t, item)
		if item.OperationKey == "desktop-thread-post:"+failed.ID.String() {
			break
		}
		require.NoError(t, outbox.FailDelivery(ctx, *item, errors.New("skip fork post")))
	}
	require.NoError(t, outbox.FailDelivery(ctx, *item, errors.New("discord unavailable")))
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
		WorkspaceID: workspaceID, RequestKey: strings.Repeat("7", 64), Params: json.RawMessage(
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
		require.NoError(t, outbox.RetryDelivery(ctx, *recoveredPost, time.Now().Add(time.Hour),
			errors.New("推迟无关投影")))
	}
	require.Contains(t, string(recoveredPost.Payload), "offline post")
	require.NotContains(t, string(recoveredPost.Payload), "second while recovering",
		"重试创建 Forum Post 必须保留最初的 Starter Message")
	completeWorkerOutbox(t, ctx, outbox, recoveredPost,
		json.RawMessage(`{"threadId":"desktop-discord-recovered",`+
			`"messageId":"desktop-recovered-starter"}`))
	failed, err = client.DesktopThreadState(ctx, failed.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", failed.Status)
	var recoveredSecondInput, recoveredFirstReply int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM integration_outbox
		WHERE operation_key LIKE $1`, "projection:desktop-input:"+failed.ConversationID.String()+
		":offline-second:%").Scan(&recoveredSecondInput))
	require.Equal(t, 1, recoveredSecondInput,
		"Post 恢复后必须补投影故障期间的后续 Desktop 输入")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM integration_outbox
		WHERE operation_key=$1`, "projection:conversation-reply:"+failed.ConversationID.String()+
		":message:"+failedTask.Claimed.ProjectionAnchor).Scan(&recoveredFirstReply))
	require.Equal(t, 1, recoveredFirstReply,
		"Post 恢复后必须补投影已经完成的最终回复")
	require.NoError(t, client.RecordSubmission(ctx, &recoveryTask, "desktop-offline-turn-2"))
	require.NoError(t, client.ConfirmTurn(ctx, &recoveryTask, "desktop-offline-turn-2"))
	require.NoError(t, client.Complete(ctx, &recoveryTask, codexcontrol.TurnResult{
		TurnID: "desktop-offline-turn-2", FinalAnswer: "offline second done",
	}))
}

func TestWorkerAPIWorkspaceProjectSnapshotSynchronizesMissingAndRecovery(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	worker, enrollment, err := server.workers.Create(ctx, "project-snapshot", []string{"discord"}, 2)
	require.NoError(t, err)
	_, credential, err := server.workers.Enroll(ctx, enrollment)
	require.NoError(t, err)
	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)
	repositoryID, _, _ := seedWorkerGitHubQueue(t, db, 302)
	workspaceID, _ := seedWorkerWorkspace(t, db, repositoryID, worker.ID)

	require.NoError(t, client.WorkspaceProjectSnapshot(ctx,
		workerprotocol.WorkspaceProjectSnapshotRequest{
			WorkspaceID: workspaceID,
			Projects: []workerprotocol.WorkspaceProjectSnapshot{
				{
					Name: "atlas", RelativePath: "workspaces/atlas", ProjectKind: "git",
					Branch: "main", HeadSHA: "atlas-head", Dirty: true,
					RemoteURL: "https://example.invalid/team/atlas.git",
				},
				{Name: "notes", RelativePath: "workspaces/notes", ProjectKind: "directory"},
			},
		}))
	var atlasID, notesID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM workspace_projects
		WHERE workspace_id=$1 AND relative_path='workspaces/atlas'`, workspaceID).
		Scan(&atlasID))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM workspace_projects
		WHERE workspace_id=$1 AND relative_path='workspaces/notes'`, workspaceID).
		Scan(&notesID))
	var availableCount, missingCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT
		count(*) FILTER (WHERE availability_status='available'),
		count(*) FILTER (WHERE availability_status='missing')
		FROM workspace_projects WHERE workspace_id=$1`, workspaceID).
		Scan(&availableCount, &missingCount))
	require.Equal(t, 2, availableCount)
	require.Equal(t, 1, missingCount, "首次完整快照必须把未出现的旧项目标为缺失")

	require.NoError(t, client.WorkspaceProjectSnapshot(ctx,
		workerprotocol.WorkspaceProjectSnapshotRequest{
			WorkspaceID: workspaceID,
			Projects: []workerprotocol.WorkspaceProjectSnapshot{
				{
					Name: "atlas", RelativePath: "workspaces/atlas", ProjectKind: "git",
					Branch: "feature", HeadSHA: "atlas-next",
				},
			},
		}))
	var notesStatus, atlasBranch string
	var scannedAt time.Time
	require.NoError(t, db.QueryRowContext(ctx, `SELECT availability_status
		FROM workspace_projects WHERE id=$1`, notesID).Scan(&notesStatus))
	require.Equal(t, "missing", notesStatus)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT branch
		FROM workspace_projects WHERE id=$1`, atlasID).Scan(&atlasBranch))
	require.Equal(t, "feature", atlasBranch)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT projects_scanned_at
		FROM worker_workspaces WHERE id=$1`, workspaceID).Scan(&scannedAt))

	require.NoError(t, client.WorkspaceProjectSnapshot(ctx,
		workerprotocol.WorkspaceProjectSnapshotRequest{
			WorkspaceID: workspaceID, Error: "container unavailable",
		}))
	var scannedAfterFailure time.Time
	var scanError string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT projects_scanned_at,project_scan_error
		FROM worker_workspaces WHERE id=$1`, workspaceID).
		Scan(&scannedAfterFailure, &scanError))
	require.Equal(t, scannedAt, scannedAfterFailure)
	require.Equal(t, "container unavailable", scanError)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT availability_status
		FROM workspace_projects WHERE id=$1`, atlasID).Scan(&notesStatus))
	require.Equal(t, "available", notesStatus,
		"扫描失败不得把未上报的项目批量标记为缺失")

	require.NoError(t, client.WorkspaceProjectSnapshot(ctx,
		workerprotocol.WorkspaceProjectSnapshotRequest{WorkspaceID: workspaceID}))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM workspace_projects
		WHERE workspace_id=$1 AND availability_status='missing'`, workspaceID).
		Scan(&missingCount))
	require.Equal(t, 3, missingCount, "成功空快照必须将完整旧快照标记为缺失")

	require.NoError(t, client.WorkspaceProjectSnapshot(ctx,
		workerprotocol.WorkspaceProjectSnapshotRequest{
			WorkspaceID: workspaceID,
			Projects: []workerprotocol.WorkspaceProjectSnapshot{
				{Name: "atlas", RelativePath: "workspaces/atlas", ProjectKind: "git"},
			},
		}))
	var recoveredID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM workspace_projects
		WHERE workspace_id=$1 AND relative_path='workspaces/atlas'
		  AND availability_status='available'`, workspaceID).Scan(&recoveredID))
	require.Equal(t, atlasID, recoveredID, "同路径恢复必须复用原项目记录")
}

func TestWorkerAPISSHConfigurationAndGitHubAgentInstructions(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	workerA, enrollmentA, err := server.workers.Create(ctx, "ssh-a", []string{"github"}, 1)
	require.NoError(t, err)
	workerB, enrollmentB, err := server.workers.Create(ctx, "ssh-b", []string{"github"}, 1)
	require.NoError(t, err)
	_, workerCredential, err := server.workers.Enroll(ctx, enrollmentA)
	require.NoError(t, err)
	client := workerprotocol.NewClient(endpoint, workerCredential, 5*time.Second)
	_, workerCredentialB, err := server.workers.Enroll(ctx, enrollmentB)
	require.NoError(t, err)
	clientB := workerprotocol.NewClient(endpoint, workerCredentialB, 5*time.Second)

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
		CredentialID: credential.ID, WorkerIDs: []uuid.UUID{workerA.ID},
	})
	require.NoError(t, err)
	_, err = server.ssh.CreateHost(ctx, sshconfig.HostInput{
		Alias: "wrong-worker", Hostname: "192.0.2.2", Port: 22, Username: "ubuntu",
		CredentialID: credential.ID, ProxyJumpHostID: &jump.ID,
		WorkerIDs: []uuid.UUID{workerB.ID},
	})
	require.ErrorContains(t, err, "相同的 Worker")
	target, err := server.ssh.CreateHost(ctx, sshconfig.HostInput{
		Alias: "target", Hostname: "192.0.2.3", Port: 2222, Username: "deploy",
		CredentialID: credential.ID, ProxyJumpHostID: &jump.ID,
		WorkerIDs: []uuid.UUID{workerA.ID},
	})
	require.NoError(t, err)
	_, err = server.ssh.UpdateHost(ctx, jump.ID, sshconfig.HostInput{
		Alias: jump.Alias, Hostname: jump.Hostname, Port: jump.Port, Username: jump.Username,
		CredentialID: credential.ID, ProxyJumpHostID: &target.ID,
		WorkerIDs: []uuid.UUID{workerA.ID},
	})
	require.ErrorContains(t, err, "循环")
	_, err = server.ssh.UpdateHost(ctx, jump.ID, sshconfig.HostInput{
		Alias: jump.Alias, Hostname: jump.Hostname, Port: jump.Port, Username: jump.Username,
		CredentialID: credential.ID, WorkerIDs: nil,
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

	require.NoError(t, server.settings.SaveGitHubAgentInstructions(ctx,
		platformsettings.GitHubAgentInstructions{Content: "# GitHub Agent\r\n"}))
	agents, err := server.settings.GitHubAgentInstructions(ctx)
	require.NoError(t, err)
	require.Equal(t, "# GitHub Agent\n", agents.Content)
	agentsRecorder := httptest.NewRecorder()
	agentsContext, _ := gin.CreateTestContext(agentsRecorder)
	agentsContext.Request = httptest.NewRequest("PUT", "/api/v1/settings/github-agent-instructions",
		strings.NewReader(`{"content":"# Managed through API\n"}`))
	agentsContext.Request.Header.Set("Content-Type", "application/json")
	server.putGitHubAgentInstructions(agentsContext)
	require.Equal(t, 204, agentsContext.Writer.Status())
	var globalAuditCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM audit_logs
		WHERE action='settings.github_agent_instructions.update'
		AND resource_id='github.agent.instructions'`).
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
	settings := platformsettings.NewService(db)
	server := &Server{cfg: config.Config{LeaseDuration: 2 * time.Second,
		CodexMaxSteersPerTurn: 5, CodexReconcileMaxAttempts: 3}, db: db,
		workers: workerregistry.NewService(db), settings: settings,
		ssh: sshconfig.NewService(db, secretStore), secrets: secretStore}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Next()
		for _, item := range c.Errors {
			t.Logf("worker API error: %v", item.Err)
		}
	})
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
			IdempotencyKey: key, Instruction: "test", ReplyPolicy: "silent"})
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, tx.Commit())
	return intentID
}

func enqueueWorkerDiscordIntent(t *testing.T, db *sql.DB, conversationID uuid.UUID,
	messageID string, _ uuid.UUID, profileID uuid.UUID,
) uuid.UUID {
	t.Helper()
	var projectID uuid.UUID
	require.NoError(t, db.QueryRow(`SELECT workspace_project_id
		FROM discord_conversations WHERE id=$1`, conversationID).Scan(&projectID))
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	intentID, inserted, err := codexcontrol.NewRepository(db, 2*time.Second).Enqueue(
		context.Background(), tx, codexcontrol.EnqueueRequest{
			SourceType: codexcontrol.SourceWorkspace, DiscordConversationID: conversationID,
			DiscordMessageID: messageID, ProjectID: projectID, AgentProfileID: profileID,
			IdempotencyKey: "discord:" + messageID,
			Instruction:    messageID, ReplyPolicy: "silent", Behavior: "steer_if_active",
		})
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, tx.Commit())
	return intentID
}

func workspaceProjectIDForForum(t *testing.T, db *sql.DB, forumID uuid.UUID) uuid.UUID {
	t.Helper()
	var projectID uuid.UUID
	require.NoError(t, db.QueryRow(`SELECT workspace_project_id
		FROM discord_forums WHERE id=$1`, forumID).Scan(&projectID))
	return projectID
}

func completeWorkerOutbox(t *testing.T, ctx context.Context,
	store *discordintegration.SQLoutbox, item *discordintegration.OutboxItem,
	response json.RawMessage,
) {
	t.Helper()
	require.NoError(t, store.RecordDelivery(ctx, item, response))
	require.NoError(t, store.Apply(ctx, *item))
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
			IdempotencyKey: key, Instruction: "stop", Operation: operation,
			ReplyPolicy: "silent"})
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, tx.Commit())
	return intentID
}

func assertPlacement(t *testing.T, db *sql.DB, itemID, intentID, expectedWorkerID uuid.UUID,
	expectedStatus string,
) {
	t.Helper()
	var itemWorker, controlWorker sql.NullString
	var status string
	require.NoError(t, db.QueryRow(`SELECT w.worker_id::text,
		c.worker_id::text, i.status FROM work_items w
		JOIN codex_turn_intents i ON i.id = $2
		JOIN codex_thread_controls c ON c.id = i.control_id WHERE w.id = $1`, itemID, intentID).
		Scan(&itemWorker, &controlWorker, &status))
	require.Equal(t, expectedStatus, status)
	if expectedWorkerID == uuid.Nil {
		require.False(t, itemWorker.Valid)
		require.False(t, controlWorker.Valid)
		return
	}
	require.Equal(t, expectedWorkerID.String(), itemWorker.String)
	require.Equal(t, expectedWorkerID.String(), controlWorker.String)
}

func seedWorkerWorkspace(t *testing.T, db *sql.DB, repositoryID,
	workerID uuid.UUID,
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
	var workspaceID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO worker_workspaces
		(guild_id, owner_discord_user_id, worker_id)
		VALUES ('worker-test-guild','worker-owner',$1)
		RETURNING id`, workerID).
		Scan(&workspaceID))
	var repositoryName, remoteURL string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT name,clone_url
		FROM repositories WHERE id=$1`, repositoryID).Scan(&repositoryName, &remoteURL))
	var projectID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_projects
		(workspace_id,relative_path,name,project_kind,availability_status,
		 branch,head_sha,dirty,remote_url,last_seen_at)
		VALUES ($1,$2,$3,'git','available','worker/test','worker-head',false,$4,now())
		RETURNING id`, workspaceID, "workspaces/"+repositoryName,
		repositoryName, remoteURL).Scan(&projectID))
	var resourceID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_resources
		(guild_id, resource_key, discord_id, kind, name, managed_marker)
		VALUES ('worker-test-guild','forum.worker','123456','forum','worker','marker')
		RETURNING id`).Scan(&resourceID))
	var forumID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_forums
		(guild_id, resource_id, forum_type, owner_discord_user_id, workspace_project_id,
		 workspace_id)
		VALUES ('worker-test-guild',$1,'workspace','worker-owner',$2,$3) RETURNING id`,
		resourceID, projectID, workspaceID).Scan(&forumID))
	return workspaceID, forumID
}
