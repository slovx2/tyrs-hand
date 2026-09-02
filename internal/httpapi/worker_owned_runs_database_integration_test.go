//go:build integration

package httpapi

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/slovx2/tyrs-hand/internal/workerregistry"
	"github.com/stretchr/testify/require"
)

func TestWorkerOwnedRunDecisionOfflineAndTerminalReplay(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	worker, enrollment, err := server.workers.Create(ctx, "worker-owned", []string{"discord"}, 2)
	require.NoError(t, err)
	_, credential, err := server.workers.Enroll(ctx, enrollment)
	require.NoError(t, err)
	require.NoError(t, server.workers.SetDefaults(ctx, workerregistry.Defaults{
		DiscordWorkerID: &worker.ID,
	}))
	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)
	require.NoError(t, client.Heartbeat(ctx, workerprotocol.HeartbeatRequest{
		WorkerVersion: "test", ProtocolVersion: workerprotocol.Version,
		SSHHostKeyFingerprint: testWorkerFingerprint(worker.ID),
	}))

	repositoryID, _, profileID := seedWorkerGitHubQueue(t, db, 700)
	_, forumID := seedWorkerWorkspace(t, db, repositoryID, worker.ID)
	conversationID := seedWorkerOwnedConversation(t, db, forumID, profileID)
	firstIntent := seedWorkerOwnedInput(t, db, conversationID, repositoryID, profileID,
		"worker-owned-1")

	claim, err := client.Claim(ctx, workerprotocol.ClaimRequest{Role: "discord"})
	require.NoError(t, err)
	require.NotNil(t, claim.Task)
	require.Equal(t, firstIntent, claim.Task.Claimed.ID)
	require.Equal(t, uuid.Nil, claim.Task.Claimed.RunID,
		"Control 不能在 Worker 决议前创建 Run")
	var runCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_runs
		WHERE primary_intent_id=$1`, firstIntent).Scan(&runCount))
	require.Zero(t, runCount)

	claim.Task.Claimed.RunID = uuid.New()
	require.NoError(t, client.DecideInput(ctx, claim.Task, "start", ""))
	require.NoError(t, client.DecideInput(ctx, claim.Task, "start", ""),
		"start ACK 丢失后必须可安全重放")
	otherWorker, otherEnrollment, err := server.workers.Create(ctx, "worker-owned-other",
		[]string{"discord"}, 1)
	require.NoError(t, err)
	_, otherCredential, err := server.workers.Enroll(ctx, otherEnrollment)
	require.NoError(t, err)
	otherClient := workerprotocol.NewClient(endpoint, otherCredential, 5*time.Second)
	_, err = otherClient.RunHeartbeat(ctx, claim.Task)
	require.Error(t, err, "其他 Worker Credential 不能操作该 Run")
	require.NotEqual(t, worker.ID, otherWorker.ID)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_runs
		WHERE primary_intent_id=$1`, firstIntent).Scan(&runCount))
	require.Equal(t, 1, runCount)
	steerIntent := seedWorkerOwnedInput(t, db, conversationID, repositoryID, profileID,
		"worker-owned-steer")
	steerClaim, err := client.Claim(ctx, workerprotocol.ClaimRequest{Role: "discord"})
	require.NoError(t, err)
	require.Equal(t, steerIntent, steerClaim.Task.Claimed.ID)
	steerClaim.Task.Claimed.RunID = claim.Task.Claimed.RunID
	require.NoError(t, client.DecideInput(ctx, steerClaim.Task, "steer", "worker-owned-turn-1"))
	require.NoError(t, client.DecideInput(ctx, steerClaim.Task, "steer", "worker-owned-turn-1"),
		"steer ACK 丢失后必须可安全重放")
	var steerStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM codex_turn_intents
		WHERE id=$1`, steerIntent).Scan(&steerStatus))
	require.Equal(t, "running", steerStatus)

	_, err = db.ExecContext(ctx, `UPDATE workers SET heartbeat_at=now()-interval '10 minutes'
		WHERE id=$1`, worker.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE codex_thread_controls SET
		lease_expires_at=now()-interval '10 minutes' WHERE id=$1`, claim.Task.Claimed.ControlID)
	require.NoError(t, err)
	requeued, err := codexcontrol.NewRepository(db, time.Second).RequeueExpired(ctx)
	require.NoError(t, err)
	require.Zero(t, requeued)
	var intentStatus, runStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT i.status,r.status
		FROM codex_turn_intents i JOIN codex_turn_runs r ON r.primary_intent_id=i.id
		WHERE i.id=$1`, firstIntent).Scan(&intentStatus, &runStatus))
	require.Equal(t, "dispatching", intentStatus)
	require.Equal(t, "starting", runStatus)

	require.NoError(t, client.Complete(ctx, claim.Task, codexcontrol.TurnResult{
		TurnID: "worker-owned-turn-1", FinalAnswer: "done",
	}))
	require.NoError(t, client.Complete(ctx, claim.Task, codexcontrol.TurnResult{
		TurnID: "worker-owned-turn-1", FinalAnswer: "done",
	}), "重复终态补传必须立即确认")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM session_messages
		WHERE local_id=$1`, "intent-result:"+firstIntent.String()).Scan(&runCount))
	require.Equal(t, 1, runCount, "重复终态不能重复写入用户可见结果")
}

func TestWorkerOwnedRunConcurrentDecisionAndLeaseExpiredRepair(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	worker, enrollment, err := server.workers.Create(ctx, "worker-concurrent", []string{"discord"}, 2)
	require.NoError(t, err)
	_, credential, err := server.workers.Enroll(ctx, enrollment)
	require.NoError(t, err)
	require.NoError(t, server.workers.SetDefaults(ctx, workerregistry.Defaults{
		DiscordWorkerID: &worker.ID,
	}))
	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)

	repositoryID, _, profileID := seedWorkerGitHubQueue(t, db, 701)
	_, forumID := seedWorkerWorkspace(t, db, repositoryID, worker.ID)
	conversationID := seedWorkerOwnedConversation(t, db, forumID, profileID)
	intentID := seedWorkerOwnedInput(t, db, conversationID, repositoryID, profileID,
		"worker-owned-concurrent")
	claim, err := client.Claim(ctx, workerprotocol.ClaimRequest{Role: "discord"})
	require.NoError(t, err)
	require.Equal(t, intentID, claim.Task.Claimed.ID)

	first, second := *claim.Task, *claim.Task
	first.Claimed.RunID, second.Claimed.RunID = uuid.New(), uuid.New()
	errorsByDecision := make(chan error, 2)
	var decisions sync.WaitGroup
	for _, task := range []*workerprotocol.Task{&first, &second} {
		decisions.Add(1)
		go func(value *workerprotocol.Task) {
			defer decisions.Done()
			errorsByDecision <- client.DecideInput(ctx, value, "start", "")
		}(task)
	}
	decisions.Wait()
	close(errorsByDecision)
	successes := 0
	for decisionErr := range errorsByDecision {
		if decisionErr == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes, "并发 start 只能有一个决议成功")

	var winningRun uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM codex_turn_runs
		WHERE primary_intent_id=$1`, intentID).Scan(&winningRun))
	winner := *claim.Task
	winner.Claimed.RunID = winningRun
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_intents SET status='failed',
		last_error_code='lease_expired',last_error_message='legacy timeout',finished_at=now()
		WHERE id=$1`, intentID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_runs SET status='failed',active_slot=NULL,
		error_code='lease_expired',error_message='legacy timeout',finished_at=now()
		WHERE id=$1`, winningRun)
	require.NoError(t, err)

	require.NoError(t, client.Complete(ctx, &winner, codexcontrol.TurnResult{
		TurnID: "recovered-turn", FinalAnswer: "worker truth",
	}))
	var intentStatus, runStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT i.status,r.status
		FROM codex_turn_intents i JOIN codex_turn_runs r ON r.primary_intent_id=i.id
		WHERE i.id=$1`, intentID).Scan(&intentStatus, &runStatus))
	require.Equal(t, "completed", intentStatus)
	require.Equal(t, "completed", runStatus)
}

func seedWorkerOwnedConversation(t *testing.T, db *sql.DB, forumID, profileID uuid.UUID) uuid.UUID {
	t.Helper()
	projectID := workspaceProjectIDForForum(t, db, forumID)
	var conversationID uuid.UUID
	require.NoError(t, db.QueryRow(`INSERT INTO discord_conversations(
		guild_id,forum_id,thread_id,starter_message_id,owner_discord_user_id,
		workspace_project_id,agent_profile_id,title,configuration_status,title_rename_status)
		VALUES ('worker-test-guild',$1,$2,$2,'worker-owner',$3,$4,'worker owned',
		'configured','completed') RETURNING id`, forumID, uuid.NewString(), projectID,
		profileID).Scan(&conversationID))
	return conversationID
}

func seedWorkerOwnedInput(t *testing.T, db *sql.DB, conversationID, repositoryID,
	profileID uuid.UUID, messageID string,
) uuid.UUID {
	t.Helper()
	_, err := db.Exec(`INSERT INTO discord_input_messages(message_id,conversation_id,
		discord_user_id,display_name,username,access_snapshot,body)
		VALUES ($1,$2,'worker-owner','Owner','owner','owner',$1)`, messageID, conversationID)
	require.NoError(t, err)
	return enqueueWorkerDiscordIntent(t, db, conversationID, messageID, repositoryID, profileID)
}
