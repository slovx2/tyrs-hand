//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pquerna/otp/totp"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/security"
	"github.com/slovx2/tyrs-hand/internal/workerregistry"
	"github.com/stretchr/testify/require"
)

func TestClientProtocolLoginIdempotencyWebSocketInteractiveAndFinalAnswer(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	authService := auth.NewService(db, box, "client-setup-token", "")
	setup, err := authService.Setup(ctx, "client-setup-token", "mobile-admin", "test-password-123")
	require.NoError(t, err)

	server, endpoint := clientIntegrationServer(t, db, authService)
	worker, _, err := server.workers.Create(ctx, "client-protocol-worker", []string{"discord"}, 4)
	require.NoError(t, err)
	repositoryID, _, profileID := seedWorkerGitHubQueue(t, db, 9901)
	workspaceID, forumID := seedWorkerWorkspace(t, db, repositoryID, worker.ID)
	projectID := workspaceProjectIDForForum(t, db, forumID)
	nativeCatalog := `{"modelCatalogs":{"` + workspaceID.String() + `":{"data":[{` +
		`"id":"native-only-model","model":"native-only-model","displayName":"Native",` +
		`"description":"from Codex","supportedReasoningEfforts":[{` +
		`"reasoningEffort":"future","description":"future effort"}],` +
		`"defaultReasoningEffort":"future","serviceTiers":[],` +
		`"additionalSpeedTiers":[],"defaultServiceTier":null,"isDefault":true,` +
		`"hidden":false}],"nextCursor":null}}}`
	_, err = db.ExecContext(ctx, `UPDATE workers SET status='online',
		heartbeat_at=now(), metadata=$2::jsonb WHERE id=$1`, worker.ID, nativeCatalog)
	require.NoError(t, err)
	var sessionID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_sessions(
		workspace_id,workspace_project_id,agent_profile_id,title)
		VALUES ($1,$2,$3,'Client protocol') RETURNING id`, workspaceID, projectID,
		profileID).Scan(&sessionID))

	code, err := totp.GenerateCode(setup.TOTPSecret, time.Now())
	require.NoError(t, err)
	login := clientJSONRequest(t, http.MethodPost, endpoint+"/api/v1/client/auth/login", "",
		map[string]any{"username": "mobile-admin", "password": "test-password-123", "totp": code})
	require.Equal(t, http.StatusOK, login.Code)
	var loginBody struct {
		AccessToken string `json:"accessToken"`
		User        struct {
			ID uuid.UUID `json:"id"`
		} `json:"user"`
	}
	require.NoError(t, json.Unmarshal(login.Body.Bytes(), &loginBody))
	require.NotEmpty(t, loginBody.AccessToken)

	bootstrap := clientJSONRequest(t, http.MethodGet, endpoint+"/api/v1/client/bootstrap",
		loginBody.AccessToken, nil)
	require.Equal(t, http.StatusOK, bootstrap.Code)
	require.Contains(t, bootstrap.Body.String(), workspaceID.String())
	require.Contains(t, bootstrap.Body.String(), projectID.String())
	require.Contains(t, bootstrap.Body.String(), profileID.String())
	require.Contains(t, bootstrap.Body.String(), `"protocolVersion":3`)
	require.Contains(t, bootstrap.Body.String(), `"modelCatalogs"`)
	require.Contains(t, bootstrap.Body.String(), "native-only-model")
	var bootstrapBody struct {
		LastStartedSettings clientLastSettings `json:"lastStartedSettings"`
	}
	require.NoError(t, json.Unmarshal(bootstrap.Body.Bytes(), &bootstrapBody))
	require.Equal(t, profileID, bootstrapBody.LastStartedSettings.AgentProfileID)
	require.Equal(t, "standard", bootstrapBody.LastStartedSettings.ServiceTier)
	require.Equal(t, "default", bootstrapBody.LastStartedSettings.CollaborationMode)
	listedSessions := clientJSONRequest(t, http.MethodGet,
		endpoint+"/api/v1/client/sessions?limit=10", loginBody.AccessToken, nil)
	require.Equal(t, http.StatusOK, listedSessions.Code, listedSessions.Body.String())
	require.Contains(t, listedSessions.Body.String(), sessionID.String())
	var listedBody struct {
		Sessions []clientSession `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal(listedSessions.Body.Bytes(), &listedBody))
	listedSession := requireClientSession(t, listedBody.Sessions, sessionID)
	require.False(t, listedSession.IsRunning)
	require.False(t, listedSession.HasRunIssue)
	require.Zero(t, listedSession.LastAgentMessageSeq)
	require.Nil(t, listedSession.PendingInteractiveID)

	wsURL := "ws" + strings.TrimPrefix(endpoint, "http") + "/api/v1/client/updates?cursor=0"
	protocol := clientBearerWebSocketPrefix + loginBody.AccessToken
	dialer := websocket.Dialer{Subprotocols: []string{protocol}}
	first, response, err := dialer.Dial(wsURL, nil)
	require.NoError(t, err)
	require.Equal(t, protocol, response.Header.Get("Sec-WebSocket-Protocol"))
	defer func() { _ = first.Close() }()
	second, _, err := dialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer func() { _ = second.Close() }()

	createdSession := clientJSONRequest(t, http.MethodPost,
		endpoint+"/api/v1/client/sessions", loginBody.AccessToken, map[string]any{
			"projectId": projectID,
			"settings": map[string]any{"agentProfileId": profileID, "model": "gpt-5.6-sol",
				"reasoningEffort": "high", "serviceTier": "standard",
				"collaborationMode": "default", "settingsVersion": 0},
			"initialMessage": map[string]any{"localId": "atomic-session-1",
				"text": "Created through client protocol", "attachmentIds": []string{}},
		})
	require.Equal(t, http.StatusCreated, createdSession.Code, createdSession.Body.String())
	var createdSessionBody struct {
		Session clientSession `json:"session"`
	}
	require.NoError(t, json.Unmarshal(createdSession.Body.Bytes(), &createdSessionBody))
	require.NotEqual(t, uuid.Nil, createdSessionBody.Session.ID)
	firstMessageUpdate := readClientUpdate(t, first)
	secondMessageUpdate := readClientUpdate(t, second)
	require.Equal(t, "message.created", firstMessageUpdate.Params.Type)
	require.Equal(t, firstMessageUpdate.Params.Cursor, secondMessageUpdate.Params.Cursor)
	firstSessionUpdate := readClientUpdate(t, first)
	secondSessionUpdate := readClientUpdate(t, second)
	require.Equal(t, "session.created", firstSessionUpdate.Params.Type)
	require.Equal(t, firstSessionUpdate.Params.Cursor, secondSessionUpdate.Params.Cursor)

	messageURL := endpoint + "/api/v1/client/sessions/" + sessionID.String() + "/messages"
	created := clientJSONRequest(t, http.MethodPost, messageURL, loginBody.AccessToken,
		map[string]any{"localId": "mobile-local-1", "text": "hello from mobile"})
	require.Equal(t, http.StatusCreated, created.Code)
	firstUpdate := readClientUpdate(t, first)
	secondUpdate := readClientUpdate(t, second)
	require.Equal(t, "message.created", firstUpdate.Params.Type)
	require.Equal(t, firstUpdate.Params.Cursor, secondUpdate.Params.Cursor)
	snapshotResponse := clientJSONRequest(t, http.MethodGet, endpoint+"/api/v1/client/sessions/"+
		sessionID.String()+"/snapshot?turnLimit=20", loginBody.AccessToken, nil)
	require.Equal(t, http.StatusOK, snapshotResponse.Code, snapshotResponse.Body.String())
	var snapshotBody clientSessionSnapshot
	require.NoError(t, json.Unmarshal(snapshotResponse.Body.Bytes(), &snapshotBody))
	require.Equal(t, sessionID, snapshotBody.Session.ID)
	require.NotEmpty(t, snapshotBody.Turns.Items)
	require.GreaterOrEqual(t, snapshotBody.SnapshotCursor, firstUpdate.Params.Cursor)

	retried := clientJSONRequest(t, http.MethodPost, messageURL, loginBody.AccessToken,
		map[string]any{"localId": "mobile-local-1", "text": "hello from mobile"})
	require.Equal(t, http.StatusOK, retried.Code)
	require.Contains(t, retried.Body.String(), `"deduplicated":true`)
	var messageCount, intentCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM session_messages
		WHERE session_id=$1 AND local_id='mobile-local-1'`, sessionID).Scan(&messageCount))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_intents
		WHERE session_id=$1`, sessionID).Scan(&intentCount))
	require.Equal(t, 1, messageCount)
	require.Equal(t, 1, intentCount)

	_ = first.Close()
	var reconnectCursor int64
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO client_updates(
		session_id,update_type,entity_id,payload) VALUES ($1,'session.updated',$2,'{}')
		RETURNING cursor`, sessionID, sessionID.String()).Scan(&reconnectCursor))
	liveUpdate := readClientUpdate(t, second)
	require.Equal(t, reconnectCursor, liveUpdate.Params.Cursor)
	reconnectURL := "ws" + strings.TrimPrefix(endpoint, "http") + "/api/v1/client/updates?cursor=" +
		fmt.Sprint(firstUpdate.Params.Cursor)
	reconnected, _, err := dialer.Dial(reconnectURL, nil)
	require.NoError(t, err)
	defer func() { _ = reconnected.Close() }()
	replayed := readClientUpdate(t, reconnected)
	require.Equal(t, reconnectCursor, replayed.Params.Cursor)

	repository := codexcontrol.NewRepository(db, time.Minute, 5, 3)
	claimed, err := repository.ClaimWorker(ctx, "client-protocol-worker",
		codexcontrol.SourceWorkspace, worker.ID)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	if claimed.SessionID != sessionID {
		require.NoError(t, repository.Complete(ctx, claimed, codexcontrol.TurnResult{
			TurnID: "atomic-turn", FinalAnswer: "atomic session completed",
		}))
		claimed, err = repository.ClaimWorker(ctx, "client-protocol-worker",
			codexcontrol.SourceWorkspace, worker.ID)
		require.NoError(t, err)
		require.NotNil(t, claimed)
	}
	require.Equal(t, sessionID, claimed.SessionID)
	snapshotWithRun := clientJSONRequest(t, http.MethodGet,
		endpoint+"/api/v1/client/sessions/"+sessionID.String()+"/snapshot?turnLimit=20",
		loginBody.AccessToken, nil)
	require.Equal(t, http.StatusOK, snapshotWithRun.Code, snapshotWithRun.Body.String())
	require.Contains(t, snapshotWithRun.Body.String(), claimed.RunID.String())
	runningSessions := clientJSONRequest(t, http.MethodGet,
		endpoint+"/api/v1/client/sessions?limit=10", loginBody.AccessToken, nil)
	require.Equal(t, http.StatusOK, runningSessions.Code)
	require.NoError(t, json.Unmarshal(runningSessions.Body.Bytes(), &listedBody))
	runningSession := requireClientSession(t, listedBody.Sessions, sessionID)
	require.True(t, runningSession.IsRunning)
	require.False(t, runningSession.HasRunIssue)
	projectionTx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	commentaryPayload := json.RawMessage(`{"item":{"id":"commentary-client-test",` +
		`"type":"agentMessage","phase":"commentary","text":"projected commentary"}}`)
	var occurredAt time.Time
	require.NoError(t, projectionTx.QueryRowContext(ctx, `INSERT INTO agent_events(
		control_id,intent_id,run_id,event_type,external_event_id,payload,run_event_sequence)
		VALUES ($1,$2,$3,'item/completed','worker:1',$4,1) RETURNING occurred_at`,
		claimed.ControlID, claimed.ID, claimed.RunID, commentaryPayload).Scan(&occurredAt))
	require.NoError(t, projectRunEventTx(ctx, projectionTx, claimed.RunID, runEventProjection{
		Sequence: 1, Type: "item/completed", Payload: commentaryPayload, OccurredAt: occurredAt,
	}))
	_, err = projectionTx.ExecContext(ctx, `UPDATE codex_turn_runs SET
		worker_event_sequence=1,client_projection_sequence=1 WHERE id=$1`,
		claimed.RunID)
	require.NoError(t, err)
	require.NoError(t, projectionTx.Commit())
	interactiveID := uuid.New()
	questions := json.RawMessage(`[ {"id":"choice","question":"Continue?","options":[]} ]`)
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_interactive_requests(
		id,control_id,run_id,session_id,thread_id,turn_id,item_id,app_server_generation,
		app_server_request_id,questions) VALUES ($1,$2,$3,$4,'thread-1','turn-1','item-1',1,
		'1'::jsonb,$5) RETURNING id`, interactiveID, claimed.ControlID, claimed.RunID,
		sessionID, questions).Scan(&interactiveID))
	require.NoError(t, db.QueryRowContext(ctx, `UPDATE codex_turn_runs
		SET status='waiting_for_user',active_slot=NULL WHERE id=$1 RETURNING id`, claimed.RunID).
		Scan(&claimed.RunID))
	waitingSessions := clientJSONRequest(t, http.MethodGet,
		endpoint+"/api/v1/client/sessions?limit=10", loginBody.AccessToken, nil)
	require.Equal(t, http.StatusOK, waitingSessions.Code)
	require.NoError(t, json.Unmarshal(waitingSessions.Body.Bytes(), &listedBody))
	waitingSession := requireClientSession(t, listedBody.Sessions, sessionID)
	require.False(t, waitingSession.IsRunning)
	require.False(t, waitingSession.HasRunIssue)
	require.Equal(t, interactiveID, *waitingSession.PendingInteractiveID)
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_runs SET status='starting',active_slot=1 WHERE id=$1`,
		claimed.RunID)
	require.NoError(t, err)

	answers := make(chan bool, 2)
	var answerWait sync.WaitGroup
	for index := 0; index < 2; index++ {
		answerWait.Add(1)
		go func(value string) {
			defer answerWait.Done()
			result := clientJSONRequest(t, http.MethodPost, endpoint+"/api/v1/client/interactive/"+
				interactiveID.String()+"/answer", loginBody.AccessToken,
				map[string]any{"answer": map[string]any{"answers": map[string]any{
					"choice": map[string]any{"answers": []string{value}},
				}}})
			require.Equal(t, http.StatusOK, result.Code)
			var body struct {
				Accepted bool `json:"accepted"`
			}
			require.NoError(t, json.Unmarshal(result.Body.Bytes(), &body))
			answers <- body.Accepted
		}(fmt.Sprintf("client-%d", index))
	}
	answerWait.Wait()
	close(answers)
	accepted := 0
	for value := range answers {
		if value {
			accepted++
		}
	}
	require.Equal(t, 1, accepted)

	cleanupInteractiveID := uuid.New()
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_interactive_requests(
		id,control_id,run_id,session_id,thread_id,turn_id,item_id,app_server_generation,
		app_server_request_id,questions) VALUES ($1,$2,$3,$4,'thread-1','turn-1',
		'cleanup-item',1,'3'::jsonb,$5) RETURNING id`, cleanupInteractiveID,
		claimed.ControlID, claimed.RunID, sessionID, questions).Scan(&cleanupInteractiveID))
	require.NoError(t, repository.Complete(ctx, claimed, codexcontrol.TurnResult{
		TurnID: "turn-1", FinalAnswer: "final answer for every client",
	}))
	var cleanupInteractiveStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM codex_interactive_requests
		WHERE id=$1`, cleanupInteractiveID).Scan(&cleanupInteractiveStatus))
	require.Equal(t, "interrupted", cleanupInteractiveStatus)
	terminalInteractiveID := uuid.New()
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_interactive_requests(
		id,control_id,run_id,session_id,thread_id,turn_id,item_id,app_server_generation,
		app_server_request_id,questions) VALUES ($1,$2,$3,$4,'thread-1','turn-1',
		'terminal-item',1,'2'::jsonb,$5) RETURNING id`, terminalInteractiveID,
		claimed.ControlID, claimed.RunID, sessionID, questions).Scan(&terminalInteractiveID))
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_runs SET status='running',active_slot=1
		WHERE id=$1`, claimed.RunID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_intents SET status='running',finished_at=NULL,
		input_surface='desktop'
		WHERE id=$1`, claimed.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE codex_thread_controls SET status='active',
		active_intent_id=$2,lease_expires_at=NULL WHERE id=$1`, claimed.ControlID, claimed.ID)
	require.NoError(t, err)
	terminalAnswer := clientJSONRequest(t, http.MethodPost, endpoint+"/api/v1/client/interactive/"+
		terminalInteractiveID.String()+"/answer", loginBody.AccessToken,
		map[string]any{"answer": map[string]any{"answers": map[string]any{
			"choice": map[string]any{"answers": []string{"继续"}},
		}}})
	require.Equal(t, http.StatusConflict, terminalAnswer.Code, terminalAnswer.Body.String())
	var terminalInteractiveStatus, terminalRunStatus string
	var terminalActiveSlot sql.NullInt64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT q.status,r.status,r.active_slot
		FROM codex_interactive_requests q JOIN codex_turn_runs r ON r.id=q.run_id
		WHERE q.id=$1`, terminalInteractiveID).Scan(&terminalInteractiveStatus,
		&terminalRunStatus, &terminalActiveSlot))
	require.Equal(t, "pending", terminalInteractiveStatus)
	require.Equal(t, "running", terminalRunStatus)
	require.True(t, terminalActiveSlot.Valid)
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_runs SET finished_at=NULL WHERE id=$1`,
		claimed.RunID)
	require.NoError(t, err)
	requeued := server.requeueExpiredRuns(ctx)
	require.Zero(t, requeued)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT q.status,r.status,r.active_slot
		FROM codex_interactive_requests q JOIN codex_turn_runs r ON r.id=q.run_id
		WHERE q.id=$1`, terminalInteractiveID).Scan(&terminalInteractiveStatus,
		&terminalRunStatus, &terminalActiveSlot))
	require.Equal(t, "pending", terminalInteractiveStatus)
	require.Equal(t, "running", terminalRunStatus)
	require.True(t, terminalActiveSlot.Valid)
	completedSessions := clientJSONRequest(t, http.MethodGet,
		endpoint+"/api/v1/client/sessions?limit=10", loginBody.AccessToken, nil)
	require.Equal(t, http.StatusOK, completedSessions.Code)
	require.NoError(t, json.Unmarshal(completedSessions.Body.Bytes(), &listedBody))
	completedSession := requireClientSession(t, listedBody.Sessions, sessionID)
	require.True(t, completedSession.IsRunning)
	require.False(t, completedSession.HasRunIssue)
	require.Greater(t, completedSession.LastAgentMessageSeq, int64(0))
	require.NotNil(t, completedSession.PendingInteractiveID)
	for _, status := range []string{"failed", "canceled"} {
		_, err = db.ExecContext(ctx, `UPDATE codex_turn_runs SET status=$2 WHERE id=$1`,
			claimed.RunID, status)
		require.NoError(t, err)
		issueSessions := clientJSONRequest(t, http.MethodGet,
			endpoint+"/api/v1/client/sessions?limit=10", loginBody.AccessToken, nil)
		require.Equal(t, http.StatusOK, issueSessions.Code)
		require.NoError(t, json.Unmarshal(issueSessions.Body.Bytes(), &listedBody))
		require.True(t, requireClientSession(t, listedBody.Sessions, sessionID).HasRunIssue)
	}
	_, err = db.ExecContext(ctx, `UPDATE codex_interactive_requests SET status='interrupted',
		resolved_at=now() WHERE id=$1`, terminalInteractiveID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_runs SET status='completed',
		active_slot=NULL,finished_at=now() WHERE id=$1`, claimed.RunID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_intents SET status='completed',
		finished_at=now() WHERE id=$1`, claimed.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE codex_thread_controls SET status='idle',
		active_intent_id=NULL WHERE id=$1`, claimed.ControlID)
	require.NoError(t, err)
	messages := clientJSONRequest(t, http.MethodGet, messageURL+"?beforeSeq=999", loginBody.AccessToken, nil)
	require.Equal(t, http.StatusOK, messages.Code)
	require.Contains(t, messages.Body.String(), "final answer for every client")
	require.Contains(t, messages.Body.String(), `"role":"agent"`)
	turns := clientJSONRequest(t, http.MethodGet, endpoint+"/api/v1/client/sessions/"+
		sessionID.String()+"/turns", loginBody.AccessToken, nil)
	require.Equal(t, http.StatusOK, turns.Code)
	require.Contains(t, turns.Body.String(), "final answer for every client")
	var turnBody struct {
		Items []clientTurnPageItem `json:"items"`
	}
	require.NoError(t, json.Unmarshal(turns.Body.Bytes(), &turnBody))
	require.NotEmpty(t, turnBody.Items)
	run := turnBody.Items[len(turnBody.Items)-1].Runs[0]
	require.NotEmpty(t, run.Segments)
	activities := clientJSONRequest(t, http.MethodGet, endpoint+"/api/v1/client/runs/"+
		run.ID.String()+"/segments/"+run.Segments[0].ID.String()+"/activities",
		loginBody.AccessToken, nil)
	require.Equal(t, http.StatusOK, activities.Code)
	require.Contains(t, activities.Body.String(), "projected commentary")
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_runs SET worker_event_sequence=2,
		client_projection_sequence=1 WHERE id=$1`, run.ID)
	require.NoError(t, err)
	unprojected := clientJSONRequest(t, http.MethodGet, endpoint+"/api/v1/client/runs/"+
		run.ID.String()+"/segments/"+run.Segments[0].ID.String()+"/activities?afterEventSeq=1",
		loginBody.AccessToken, nil)
	require.Equal(t, http.StatusOK, unprojected.Code, unprojected.Body.String())
	var unprojectedBody struct {
		PersistedThroughEventSeq int64 `json:"persistedThroughEventSeq"`
	}
	require.NoError(t, json.Unmarshal(unprojected.Body.Bytes(), &unprojectedBody))
	require.Equal(t, int64(1), unprojectedBody.PersistedThroughEventSeq,
		"未投影的 Worker event 不得被客户端游标标记为已消费")
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_runs SET worker_event_sequence=1 WHERE id=$1`, run.ID)
	require.NoError(t, err)

	planContent := "# 移动端执行计划\n\n1. 修改实现\n2. 运行测试"
	var planIntentID, planRunID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `WITH sequence AS (
		UPDATE codex_thread_controls SET next_sequence_no=next_sequence_no+1,
			collaboration_mode='plan',updated_at=now() WHERE id=$1
			RETURNING next_sequence_no-1 AS value)
		INSERT INTO codex_turn_intents(control_id,sequence_no,behavior,source_type,input_surface,
			session_id,workspace_project_id,agent_profile_id,idempotency_key,instruction,status,
			result,finished_at)
		SELECT control.id,sequence.value,'start_when_idle','workspace_session','client',
			control.session_id,control.workspace_project_id,control.agent_profile_id,$2,'create plan',
			'completed',jsonb_build_object('finalAnswer',$3::text,'finalOutputType','plan'),now()
		FROM codex_thread_controls control CROSS JOIN sequence WHERE control.id=$1 RETURNING id`,
		claimed.ControlID, "client-plan-source:"+uuid.NewString(), planContent).Scan(&planIntentID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_turn_runs(
		control_id,primary_intent_id,attempt,lease_owner,lease_epoch,capability_hash,status,
		worker_id,collaboration_mode,started_at,finished_at)
		VALUES ($1,$2,1,'client-plan-test',1,$3,'completed',$4,'plan',now(),now()) RETURNING id`,
		claimed.ControlID, planIntentID, strings.Repeat("b", 64), worker.ID).Scan(&planRunID))
	_, err = db.ExecContext(ctx, `UPDATE workspace_sessions SET collaboration_mode='plan',
		settings_version=8,updated_at=now() WHERE id=$1`, sessionID)
	require.NoError(t, err)
	var conversationID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_conversations(
		guild_id,forum_id,thread_id,owner_discord_user_id,workspace_project_id,
		agent_profile_id,title,session_id,collaboration_mode,settings_revision)
		VALUES ('worker-test-guild',$1,'client-plan-thread','worker-owner',$2,$3,
		'Client protocol',$4,'plan',8) RETURNING id`, forumID, projectID, profileID, sessionID).
		Scan(&conversationID))
	_, err = db.ExecContext(ctx, `UPDATE codex_thread_controls SET
		discord_conversation_id=$2,collaboration_mode='plan',settings_revision=8 WHERE id=$1`,
		claimed.ControlID, conversationID)
	require.NoError(t, err)
	executedPlan := clientJSONRequest(t, http.MethodPost, endpoint+"/api/v1/client/sessions/"+
		sessionID.String()+"/plans/"+planRunID.String()+"/execute", loginBody.AccessToken, nil)
	require.Equal(t, http.StatusCreated, executedPlan.Code, executedPlan.Body.String())
	var executionInstruction, executionMessage string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT intent.instruction,
		message.content #>> '{v,content,data,message}' FROM codex_turn_intents intent
		JOIN session_messages message ON message.turn_intent_id=intent.id
		WHERE intent.idempotency_key=$1`, "client:plan:"+sessionID.String()+":"+planRunID.String()).
		Scan(&executionInstruction, &executionMessage))
	require.Equal(t, codexcontrol.PlanExecutionInstruction(planContent), executionInstruction)
	require.Equal(t, codexcontrol.PlanExecutionDisplayText, executionMessage)
	var sessionMode, controlMode, discordMode string
	var sessionVersion, controlRevision, discordRevision int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT session.collaboration_mode,
		session.settings_version,control.collaboration_mode,control.settings_revision,
		conversation.collaboration_mode,conversation.settings_revision
		FROM workspace_sessions session
		JOIN codex_thread_controls control ON control.session_id=session.id
		JOIN discord_conversations conversation ON conversation.session_id=session.id
		WHERE session.id=$1`, sessionID).Scan(&sessionMode, &sessionVersion, &controlMode,
		&controlRevision, &discordMode, &discordRevision))
	require.Equal(t, "default", sessionMode)
	require.Equal(t, "default", controlMode)
	require.Equal(t, "default", discordMode)
	require.EqualValues(t, 9, sessionVersion)
	require.Equal(t, sessionVersion, controlRevision)
	require.Equal(t, sessionVersion, discordRevision)
	_, err = db.ExecContext(ctx, `UPDATE workspace_sessions SET collaboration_mode='plan'
		WHERE id=$1`, sessionID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE codex_thread_controls SET collaboration_mode='plan'
		WHERE session_id=$1`, sessionID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE discord_conversations SET collaboration_mode='plan'
		WHERE session_id=$1`, sessionID)
	require.NoError(t, err)
	retriedPlan := clientJSONRequest(t, http.MethodPost, endpoint+"/api/v1/client/sessions/"+
		sessionID.String()+"/plans/"+planRunID.String()+"/execute", loginBody.AccessToken, nil)
	require.Equal(t, http.StatusOK, retriedPlan.Code, retriedPlan.Body.String())
	require.Contains(t, retriedPlan.Body.String(), `"deduplicated":true`)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT session.collaboration_mode,
		control.collaboration_mode,conversation.collaboration_mode
		FROM workspace_sessions session
		JOIN codex_thread_controls control ON control.session_id=session.id
		JOIN discord_conversations conversation ON conversation.session_id=session.id
		WHERE session.id=$1`, sessionID).Scan(&sessionMode, &controlMode, &discordMode))
	require.Equal(t, "default", sessionMode)
	require.Equal(t, "default", controlMode)
	require.Equal(t, "default", discordMode)

	planExecution, err := repository.ClaimWorker(ctx, "client-plan-execution-worker",
		codexcontrol.SourceWorkspace, worker.ID)
	require.NoError(t, err)
	require.NotNil(t, planExecution)
	require.Equal(t, "default", planExecution.CollaborationMode)
	_, err = db.ExecContext(ctx, `UPDATE discord_conversations SET
		collaboration_mode='plan',settings_revision=99 WHERE id=$1`, conversationID)
	require.NoError(t, err)
	staleProjectionMessage := clientJSONRequest(t, http.MethodPost, messageURL,
		loginBody.AccessToken, map[string]any{"localId": "stale-discord-projection",
			"text": "Continue after plan", "behavior": "steer_if_active"})
	require.Equal(t, http.StatusCreated, staleProjectionMessage.Code,
		staleProjectionMessage.Body.String())
	require.NoError(t, db.QueryRowContext(ctx, `SELECT session.collaboration_mode,
		control.collaboration_mode,conversation.collaboration_mode
		FROM workspace_sessions session
		JOIN codex_thread_controls control ON control.session_id=session.id
		JOIN discord_conversations conversation ON conversation.session_id=session.id
		WHERE session.id=$1`, sessionID).Scan(&sessionMode, &controlMode, &discordMode))
	require.Equal(t, "default", sessionMode)
	require.Equal(t, "default", controlMode)
	require.Equal(t, "default", discordMode)
	manualTitle := "Mobile and Desktop shared title"
	renamed := clientJSONRequest(t, http.MethodPatch, endpoint+"/api/v1/client/sessions/"+
		sessionID.String(), loginBody.AccessToken, map[string]any{"title": manualTitle})
	require.Equal(t, http.StatusOK, renamed.Code, renamed.Body.String())
	var sessionTitle, desiredTitle, discordTitle string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT session.title,
		control.desired_thread_name,conversation.title
		FROM workspace_sessions session
		JOIN codex_thread_controls control ON control.session_id=session.id
		JOIN discord_conversations conversation ON conversation.session_id=session.id
		WHERE session.id=$1`, sessionID).Scan(&sessionTitle, &desiredTitle, &discordTitle))
	require.Equal(t, manualTitle, sessionTitle)
	require.Equal(t, manualTitle, desiredTitle)
	require.Equal(t, manualTitle, discordTitle)
	var mobileExpectedVersion, discordExpectedRevision int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT session.settings_version,
		conversation.settings_revision FROM workspace_sessions session
		JOIN discord_conversations conversation ON conversation.session_id=session.id
		WHERE session.id=$1`, sessionID).Scan(&mobileExpectedVersion, &discordExpectedRevision))
	startConcurrentSettings := make(chan struct{})
	mobileStatus := make(chan int, 1)
	discordResult := make(chan error, 1)
	go func() {
		<-startConcurrentSettings
		body, _ := json.Marshal(map[string]any{"collaborationMode": "plan",
			"expectedSettingsVersion": mobileExpectedVersion})
		request, requestErr := http.NewRequestWithContext(context.Background(), http.MethodPatch,
			endpoint+"/api/v1/client/sessions/"+sessionID.String(), bytes.NewReader(body))
		if requestErr != nil {
			mobileStatus <- 0
			return
		}
		request.Header.Set("Authorization", "Bearer "+loginBody.AccessToken)
		request.Header.Set("Content-Type", "application/json")
		response, requestErr := http.DefaultClient.Do(request)
		if requestErr != nil {
			mobileStatus <- 0
			return
		}
		_ = response.Body.Close()
		mobileStatus <- response.StatusCode
	}()
	go func() {
		<-startConcurrentSettings
		_, updateErr := discordintegration.NewConversationService(db).SetConversationMode(ctx,
			"worker-test-guild", "client-plan-thread", "worker-owner", conversationID,
			discordExpectedRevision, "plan")
		discordResult <- updateErr
	}()
	close(startConcurrentSettings)
	statusCode := <-mobileStatus
	require.Contains(t, []int{http.StatusOK, http.StatusConflict}, statusCode)
	require.NoError(t, <-discordResult)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT session.collaboration_mode,
		control.collaboration_mode,conversation.collaboration_mode
		FROM workspace_sessions session
		JOIN codex_thread_controls control ON control.session_id=session.id
		JOIN discord_conversations conversation ON conversation.session_id=session.id
		WHERE session.id=$1`, sessionID).Scan(&sessionMode, &controlMode, &discordMode))
	require.Equal(t, "plan", sessionMode)
	require.Equal(t, "plan", controlMode)
	require.Equal(t, "plan", discordMode)
}

func TestClientMessageSkipsPendingInteractiveAndContinues(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	authService := auth.NewService(db, box, "client-skip-setup-token", "")
	setup, err := authService.Setup(ctx, "client-skip-setup-token", "mobile-admin",
		"test-password-123")
	require.NoError(t, err)
	server, endpoint := clientIntegrationServer(t, db, authService)
	worker, _, err := server.workers.Create(ctx, "client-skip-worker", []string{"discord"}, 2)
	require.NoError(t, err)
	repositoryID, _, profileID := seedWorkerGitHubQueue(t, db, 9911)
	workspaceID, forumID := seedWorkerWorkspace(t, db, repositoryID, worker.ID)
	projectID := workspaceProjectIDForForum(t, db, forumID)
	var sessionID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_sessions(
		workspace_id,workspace_project_id,agent_profile_id,title)
		VALUES ($1,$2,$3,'Client interactive skip') RETURNING id`, workspaceID, projectID,
		profileID).Scan(&sessionID))
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	repository := codexcontrol.NewRepository(db, time.Minute)
	_, inserted, err := repository.Enqueue(ctx, tx, codexcontrol.EnqueueRequest{
		SourceType: codexcontrol.SourceWorkspace, SessionID: sessionID,
		InputSurface: "client", IdempotencyKey: "client-skip-initial",
		Instruction: "Ask before continuing", Behavior: "start_when_idle",
		ActorLogin: "mobile-admin", ActorPermission: "owner",
	})
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, tx.Commit())
	claimed, err := repository.ClaimWorker(ctx, "client-skip-worker",
		codexcontrol.SourceWorkspace, worker.ID)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	interactiveID := uuid.New()
	questions := json.RawMessage(`[{"id":"choice","question":"Continue?","options":[]}]`)
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_interactive_requests(
		id,control_id,run_id,session_id,thread_id,turn_id,item_id,app_server_generation,
		app_server_request_id,questions) VALUES ($1,$2,$3,$4,'thread-skip','turn-skip',
		'item-skip',1,'1'::jsonb,$5) RETURNING id`, interactiveID, claimed.ControlID,
		claimed.RunID, sessionID, questions).Scan(&interactiveID))
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_runs SET status='waiting_for_user',
		active_slot=NULL WHERE id=$1`, claimed.RunID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE codex_turn_intents SET status='waiting_for_user'
		WHERE id=$1`, claimed.ID)
	require.NoError(t, err)

	code, err := totp.GenerateCode(setup.TOTPSecret, time.Now())
	require.NoError(t, err)
	login := clientJSONRequest(t, http.MethodPost, endpoint+"/api/v1/client/auth/login", "",
		map[string]any{"username": "mobile-admin", "password": "test-password-123", "totp": code})
	require.Equal(t, http.StatusOK, login.Code)
	var loginBody struct {
		AccessToken string `json:"accessToken"`
	}
	require.NoError(t, json.Unmarshal(login.Body.Bytes(), &loginBody))
	messageURL := endpoint + "/api/v1/client/sessions/" + sessionID.String() + "/messages"
	sent := clientJSONRequest(t, http.MethodPost, messageURL, loginBody.AccessToken,
		map[string]any{"localId": "skip-and-continue-1", "text": "Use the default choice",
			"behavior": "steer_if_active"})
	require.Equal(t, http.StatusCreated, sent.Code, sent.Body.String())

	var interactiveStatus, answer, surface, runStatus string
	var activeSlot sql.NullInt64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT request.status,request.answer::text,
		request.answer_surface,run.status,run.active_slot
		FROM codex_interactive_requests request
		JOIN codex_turn_runs run ON run.id=request.run_id WHERE request.id=$1`, interactiveID).
		Scan(&interactiveStatus, &answer, &surface, &runStatus, &activeSlot))
	require.Equal(t, "resolved", interactiveStatus)
	require.JSONEq(t, `{"answers":{}}`, answer)
	require.Equal(t, "client", surface)
	require.Equal(t, "running", runStatus)
	require.True(t, activeSlot.Valid)
	require.EqualValues(t, 1, activeSlot.Int64)
	var segmentCount, resolvedUpdateCount, messageCount, newIntentCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM run_process_segments
		WHERE interactive_request_id=$1`, interactiveID).Scan(&segmentCount))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM client_updates
		WHERE update_type='interactive.resolved' AND entity_id=$1`, interactiveID.String()).
		Scan(&resolvedUpdateCount))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM session_messages
		WHERE session_id=$1 AND local_id='skip-and-continue-1'`, sessionID).Scan(&messageCount))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_intents
		WHERE session_id=$1 AND idempotency_key=$2`, sessionID,
		"client:message:"+sessionID.String()+":skip-and-continue-1").Scan(&newIntentCount))
	require.Equal(t, 1, segmentCount)
	require.Equal(t, 1, resolvedUpdateCount)
	require.Equal(t, 1, messageCount)
	require.Equal(t, 1, newIntentCount)

	retried := clientJSONRequest(t, http.MethodPost, messageURL, loginBody.AccessToken,
		map[string]any{"localId": "skip-and-continue-1", "text": "Use the default choice",
			"behavior": "steer_if_active"})
	require.Equal(t, http.StatusOK, retried.Code)
	require.Contains(t, retried.Body.String(), `"deduplicated":true`)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM client_updates
		WHERE update_type='interactive.resolved' AND entity_id=$1`, interactiveID.String()).
		Scan(&resolvedUpdateCount))
	require.Equal(t, 1, resolvedUpdateCount)
}

func requireClientSession(t *testing.T, sessions []clientSession, id uuid.UUID) clientSession {
	t.Helper()
	for _, session := range sessions {
		if session.ID == id {
			return session
		}
	}
	require.FailNow(t, "会话列表缺少预期 Session", id.String())
	return clientSession{}
}

func clientIntegrationServer(t *testing.T, db *sql.DB, authService *auth.Service) (*Server, string) {
	t.Helper()
	server := &Server{cfg: config.Config{LeaseDuration: time.Minute, PublicURL: "http://127.0.0.1"},
		db: db, auth: authService, workers: workerregistry.NewService(db),
		clientUpdateHub: newClientUpdateHub()}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/client/auth/login", server.clientLogin)
	client := router.Group("/api/v1/client")
	client.Use(server.requireClientBearer())
	client.GET("/bootstrap", server.clientBootstrap)
	client.GET("/sessions", server.clientListSessions)
	client.POST("/sessions", server.clientCreateSession)
	client.PATCH("/sessions/:id", server.clientPatchSession)
	client.POST("/sessions/:id/archive", server.clientArchiveSession)
	client.POST("/sessions/:id/restore", server.clientRestoreSession)
	client.GET("/sessions/:id/messages", server.clientListMessages)
	client.POST("/sessions/:id/messages", server.clientCreateMessage)
	client.GET("/sessions/:id/turns", server.clientListTurns)
	client.GET("/sessions/:id/snapshot", server.clientGetSessionSnapshot)
	client.GET("/sessions/:id/turns/:turnId", server.clientGetTurn)
	client.POST("/sessions/:id/plans/:runId/execute", server.clientExecutePlan)
	client.GET("/runs/:runId/segments/:segmentId/activities", server.clientListRunActivities)
	client.POST("/interactive/:id/answer", server.clientAnswerInteractive)
	client.GET("/updates", server.clientUpdates)
	httpServer := httptest.NewServer(router)
	t.Cleanup(httpServer.Close)
	return server, httpServer.URL
}

func clientJSONRequest(t *testing.T, method, url, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		require.NoError(t, err)
	}
	request, err := http.NewRequestWithContext(context.Background(), method, url,
		bytes.NewReader(encoded))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	httpResponse, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer func() { _ = httpResponse.Body.Close() }()
	response := httptest.NewRecorder()
	response.Code = httpResponse.StatusCode
	_, err = io.Copy(response.Body, httpResponse.Body)
	require.NoError(t, err)
	return response
}

func readClientUpdate(t *testing.T, connection *websocket.Conn) clientRPCNotification {
	t.Helper()
	require.NoError(t, connection.SetReadDeadline(time.Now().Add(3*time.Second)))
	var result clientRPCNotification
	require.NoError(t, connection.ReadJSON(&result))
	return result
}

func TestConcurrentClientsSerializeMessagesWithinSession(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, _ := workerTestServer(t, db)
	worker, _, err := server.workers.Create(ctx, "client-session-worker", []string{"discord"}, 8)
	require.NoError(t, err)
	repositoryID, _, profileID := seedWorkerGitHubQueue(t, db, 8801)
	workspaceID, forumID := seedWorkerWorkspace(t, db, repositoryID, worker.ID)
	projectID := workspaceProjectIDForForum(t, db, forumID)
	var sessionID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_sessions(
		workspace_id,workspace_project_id,agent_profile_id,title)
		VALUES ($1,$2,$3,'Concurrent clients') RETURNING id`, workspaceID, projectID,
		profileID).Scan(&sessionID))

	const clients = 8
	start := make(chan struct{})
	errorsByClient := make(chan error, clients)
	var wait sync.WaitGroup
	for index := 0; index < clients; index++ {
		wait.Add(1)
		go func(client int) {
			defer wait.Done()
			<-start
			tx, beginErr := db.BeginTx(ctx, nil)
			if beginErr != nil {
				errorsByClient <- beginErr
				return
			}
			defer func() { _ = tx.Rollback() }()
			localID := fmt.Sprintf("client-%d", client)
			_, inserted, enqueueErr := codexcontrol.NewRepository(db, time.Minute).Enqueue(ctx,
				tx, codexcontrol.EnqueueRequest{
					SourceType: codexcontrol.SourceWorkspace, SessionID: sessionID,
					InputSurface: "client", IdempotencyKey: "client:message:" + localID,
					MessageLocalID: localID, Instruction: localID,
					Behavior: "start_when_idle", ReplyPolicy: "silent",
				})
			if enqueueErr == nil && !inserted {
				enqueueErr = fmt.Errorf("client %d 未插入 intent", client)
			}
			if enqueueErr == nil {
				enqueueErr = tx.Commit()
			}
			errorsByClient <- enqueueErr
		}(index)
	}
	close(start)
	wait.Wait()
	close(errorsByClient)
	for clientErr := range errorsByClient {
		require.NoError(t, clientErr)
	}

	var controls, intents, messages int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM codex_thread_controls
		WHERE session_id=$1`, sessionID).Scan(&controls))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM codex_turn_intents
		WHERE session_id=$1`, sessionID).Scan(&intents))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM session_messages
		WHERE session_id=$1`, sessionID).Scan(&messages))
	require.Equal(t, 1, controls)
	require.Equal(t, clients, intents)
	require.Equal(t, clients, messages)
	var firstSeq, lastSeq, distinctSeq int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT min(seq),max(seq),count(DISTINCT seq)
		FROM session_messages WHERE session_id=$1`, sessionID).
		Scan(&firstSeq, &lastSeq, &distinctSeq))
	require.Equal(t, 1, firstSeq)
	require.Equal(t, clients, lastSeq)
	require.Equal(t, clients, distinctSeq)
}
