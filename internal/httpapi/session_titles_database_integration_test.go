//go:build integration

package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestSessionTitleTaskLifecycleAndManualRenameFence(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	worker, enrollment, err := server.workers.Create(ctx, "title-worker", []string{"discord"}, 2)
	require.NoError(t, err)
	_, credential, err := server.workers.Enroll(ctx, enrollment)
	require.NoError(t, err)
	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)
	repositoryID, _, profileID := seedWorkerGitHubQueue(t, db, 8801)
	workspaceID, forumID := seedWorkerWorkspace(t, db, repositoryID, worker.ID)
	projectID := workspaceProjectIDForForum(t, db, forumID)

	created := enqueueTitleSession(t, db, workspaceID, projectID, profileID,
		"这是需要 Luna 重命名的第一条消息")
	claim, err := client.ClaimSessionTitle(ctx)
	require.NoError(t, err)
	require.NotNil(t, claim.Task)
	require.Equal(t, created, claim.Task.SessionID)
	require.Equal(t, 1, claim.Task.Attempt)
	require.NoError(t, client.CompleteSessionTitle(ctx, claim.Task.ID,
		workerprotocol.SessionTitleCompleteRequest{LeaseToken: claim.Task.LeaseToken,
			TitleRevision: claim.Task.TitleRevision, Title: "Luna 生成标题"}))
	var title, source, desired string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT session.title,session.title_source,
		control.desired_thread_name FROM workspace_sessions session
		JOIN codex_thread_controls control ON control.session_id=session.id WHERE session.id=$1`,
		created).Scan(&title, &source, &desired))
	require.Equal(t, "Luna 生成标题", title)
	require.Equal(t, "generated", source)
	require.Equal(t, title, desired)
	var updateType string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT update_type FROM client_updates
		WHERE session_id=$1 AND update_type='session.updated' ORDER BY cursor DESC LIMIT 1`,
		created).Scan(&updateType))

	manual := enqueueTitleSession(t, db, workspaceID, projectID, profileID, "手动改名竞争")
	manualClaim, err := client.ClaimSessionTitle(ctx)
	require.NoError(t, err)
	require.NotNil(t, manualClaim.Task)
	_, err = db.ExecContext(ctx, `UPDATE workspace_sessions SET title='人工标题',
		title_source='manual',title_revision=title_revision+1 WHERE id=$1`, manual)
	require.NoError(t, err)
	require.NoError(t, client.CompleteSessionTitle(ctx, manualClaim.Task.ID,
		workerprotocol.SessionTitleCompleteRequest{LeaseToken: manualClaim.Task.LeaseToken,
			TitleRevision: manualClaim.Task.TitleRevision, Title: "过期 Luna 标题"}))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT title,title_source FROM workspace_sessions
		WHERE id=$1`, manual).Scan(&title, &source))
	require.Equal(t, "人工标题", title)
	require.Equal(t, "manual", source)
}

func TestSessionTitleTaskLeaseRecoveryAndTerminalFallback(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	worker, enrollment, err := server.workers.Create(ctx, "title-retry-worker", []string{"discord"}, 2)
	require.NoError(t, err)
	_, credential, err := server.workers.Enroll(ctx, enrollment)
	require.NoError(t, err)
	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)
	repositoryID, _, profileID := seedWorkerGitHubQueue(t, db, 8802)
	workspaceID, forumID := seedWorkerWorkspace(t, db, repositoryID, worker.ID)
	projectID := workspaceProjectIDForForum(t, db, forumID)
	sessionID := enqueueTitleSession(t, db, workspaceID, projectID, profileID, "需要重试的标题")

	first, err := client.ClaimSessionTitle(ctx)
	require.NoError(t, err)
	require.NotNil(t, first.Task)
	_, err = db.ExecContext(ctx, `UPDATE workspace_session_title_tasks
		SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, first.Task.ID)
	require.NoError(t, err)
	second, err := client.ClaimSessionTitle(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, second.Task.Attempt)
	require.NoError(t, client.FailSessionTitle(ctx, second.Task.ID,
		workerprotocol.SessionTitleFailRequest{LeaseToken: second.Task.LeaseToken,
			ErrorCode: "generation_failed"}))
	_, err = db.ExecContext(ctx, `UPDATE workspace_session_title_tasks SET next_attempt_at=now()
		WHERE id=$1`, second.Task.ID)
	require.NoError(t, err)
	third, err := client.ClaimSessionTitle(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, third.Task.Attempt)
	require.NoError(t, client.FailSessionTitle(ctx, third.Task.ID,
		workerprotocol.SessionTitleFailRequest{LeaseToken: third.Task.LeaseToken,
			ErrorCode: "invalid_output"}))
	var status, source string
	var attempts int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT task.status,task.attempt_count,
		session.title_source FROM workspace_session_title_tasks task
		JOIN workspace_sessions session ON session.id=task.session_id WHERE task.session_id=$1`,
		sessionID).Scan(&status, &attempts, &source))
	require.Equal(t, "failed", status)
	require.Equal(t, 3, attempts)
	require.Equal(t, "fallback", source)
}

func TestDesktopContinuesWorkspaceSessionWithoutDiscordForum(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	worker, enrollment, err := server.workers.Create(ctx, "workspace-desktop-worker",
		[]string{"discord"}, 2)
	require.NoError(t, err)
	_, credential, err := server.workers.Enroll(ctx, enrollment)
	require.NoError(t, err)
	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)
	require.NoError(t, client.Heartbeat(ctx, workerprotocol.HeartbeatRequest{
		WorkerVersion: "test", ProtocolVersion: workerprotocol.Version,
		SSHHostKeyFingerprint: testWorkerFingerprint(worker.ID)}))
	repositoryID, _, profileID := seedWorkerGitHubQueue(t, db, 8803)
	workspaceID, forumID := seedWorkerWorkspace(t, db, repositoryID, worker.ID)
	projectID := workspaceProjectIDForForum(t, db, forumID)
	var sessionID, controlID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_sessions(
		workspace_id,workspace_project_id,agent_profile_id,title)
		VALUES ($1,$2,$3,'Mobile workspace session') RETURNING id`, workspaceID, projectID,
		profileID).Scan(&sessionID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_thread_controls(
		source_type,session_id,workspace_project_id,agent_profile_id,worker_id,workspace_id,
		external_thread_id)
		VALUES ('workspace_session',$1,$2,$3,$4,$5,'mobile-thread-without-forum') RETURNING id`,
		sessionID, projectID, profileID, worker.ID, workspaceID).Scan(&controlID))

	task, err := client.PrepareDesktopTurn(ctx, workerprotocol.DesktopTurnPrepareRequest{
		WorkspaceID: workspaceID, RunID: uuid.New(), IntentID: uuid.New(),
		RequestKey: strings.Repeat("c", 64),
		Params: json.RawMessage(`{"threadId":"mobile-thread-without-forum",` +
			`"clientUserMessageId":"desktop-new-turn",` +
			`"input":[{"type":"text","text":"desktop cross-device message"}]}`),
	})
	require.NoError(t, err)
	require.Equal(t, controlID, task.Claimed.ControlID)
	require.Equal(t, uuid.Nil, task.Claimed.DiscordConversationID)
	require.NotNil(t, task.Snapshot.Session)
	require.NoError(t, client.RecordSubmission(ctx, &task, "desktop-active-turn"))
	require.NoError(t, client.ConfirmTurn(ctx, &task, "desktop-active-turn"))
	require.NoError(t, client.RecordDesktopSteer(ctx, workerprotocol.DesktopSteerRecordRequest{
		WorkspaceID: workspaceID, RequestKey: strings.Repeat("d", 64),
		Params: json.RawMessage(`{"threadId":"mobile-thread-without-forum",` +
			`"expectedTurnId":"desktop-active-turn","clientUserMessageId":"desktop-steer",` +
			`"input":[{"type":"text","text":"desktop steer message"}]}`),
	}))
	rows, err := db.QueryContext(ctx, `SELECT content->'v'->'content'->'data'->>'message',
		conversation_turn_id FROM session_messages WHERE session_id=$1 ORDER BY seq`, sessionID)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var messages []string
	var turnIDs []uuid.UUID
	for rows.Next() {
		var message string
		var turnID uuid.UUID
		require.NoError(t, rows.Scan(&message, &turnID))
		messages = append(messages, message)
		turnIDs = append(turnIDs, turnID)
	}
	require.Equal(t, []string{"desktop cross-device message", "desktop steer message"}, messages)
	require.Len(t, turnIDs, 2)
	require.Equal(t, turnIDs[0], turnIDs[1], "活动 Run steer 必须归入同一轮")
	var updatePayload string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT payload::text FROM client_updates
		WHERE session_id=$1 AND update_type='message.created' ORDER BY cursor DESC LIMIT 1`,
		sessionID).Scan(&updatePayload))
	require.Contains(t, updatePayload, `"conversationTurnId"`)
}

func enqueueTitleSession(t *testing.T, db *sql.DB, workspaceID, projectID,
	profileID uuid.UUID, message string,
) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var sessionID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_sessions(
		workspace_id,workspace_project_id,agent_profile_id,title,title_source)
		VALUES ($1,$2,$3,'fallback','fallback') RETURNING id`, workspaceID, projectID,
		profileID).Scan(&sessionID))
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, inserted, err := codexcontrol.NewRepository(db, 2*time.Second).Enqueue(ctx, tx,
		codexcontrol.EnqueueRequest{SourceType: codexcontrol.SourceWorkspace,
			SessionID: sessionID, MessageLocalID: "first:" + uuid.NewString(),
			InputSurface: "client", IdempotencyKey: "title:" + uuid.NewString(),
			Instruction: message, Behavior: "start_when_idle", ReplyPolicy: "silent"})
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, tx.Commit())
	return sessionID
}
