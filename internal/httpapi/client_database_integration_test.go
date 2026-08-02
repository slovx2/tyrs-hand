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
	"github.com/slovx2/tyrs-hand/internal/executionnode"
	"github.com/slovx2/tyrs-hand/internal/security"
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
	node, _, err := server.nodes.Create(ctx, "client-protocol-node", []string{"discord"}, 4)
	require.NoError(t, err)
	repositoryID, _, profileID := seedWorkerGitHubQueue(t, db, 9901)
	environmentID, forumID := seedDevelopmentOperation(t, db, repositoryID, node.ID)
	projectID := developmentProjectIDForForum(t, db, forumID)
	var sessionID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO development_sessions(
		development_environment_id,development_project_id,agent_profile_id,title)
		VALUES ($1,$2,$3,'Client protocol') RETURNING id`, environmentID, projectID,
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
			"developmentEnvironmentId": environmentID,
			"developmentProjectId":     projectID,
			"agentProfileId":           profileID,
			"title":                    "Created through client protocol",
		})
	require.Equal(t, http.StatusCreated, createdSession.Code)
	var createdSessionBody clientSession
	require.NoError(t, json.Unmarshal(createdSession.Body.Bytes(), &createdSessionBody))
	require.NotEqual(t, uuid.Nil, createdSessionBody.ID)
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
	claimed, err := repository.ClaimNode(ctx, "client-protocol-worker",
		codexcontrol.SourceDevelopment, node.ID)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	interactiveID := uuid.New()
	questions := json.RawMessage(`[ {"id":"choice","question":"Continue?","options":[]} ]`)
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_interactive_requests(
		id,control_id,run_id,session_id,thread_id,turn_id,item_id,app_server_generation,
		app_server_request_id,questions) VALUES ($1,$2,$3,$4,'thread-1','turn-1','item-1',1,
		'1'::jsonb,$5) RETURNING id`, interactiveID, claimed.ControlID, claimed.RunID,
		sessionID, questions).Scan(&interactiveID))

	answers := make(chan bool, 2)
	var answerWait sync.WaitGroup
	for index := 0; index < 2; index++ {
		answerWait.Add(1)
		go func(value string) {
			defer answerWait.Done()
			result := clientJSONRequest(t, http.MethodPost, endpoint+"/api/v1/client/interactive/"+
				interactiveID.String()+"/answer", loginBody.AccessToken,
				map[string]any{"answer": map[string]any{"answers": map[string]any{"choice": value}}})
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

	require.NoError(t, repository.Complete(ctx, claimed, codexcontrol.TurnResult{
		TurnID: "turn-1", FinalAnswer: "final answer for every client",
	}))
	messages := clientJSONRequest(t, http.MethodGet, messageURL+"?beforeSeq=999", loginBody.AccessToken, nil)
	require.Equal(t, http.StatusOK, messages.Code)
	require.Contains(t, messages.Body.String(), "final answer for every client")
	require.Contains(t, messages.Body.String(), `"role":"agent"`)
}

func clientIntegrationServer(t *testing.T, db *sql.DB, authService *auth.Service) (*Server, string) {
	t.Helper()
	server := &Server{cfg: config.Config{LeaseDuration: time.Minute, PublicURL: "http://127.0.0.1"},
		db: db, auth: authService, nodes: executionnode.NewService(db),
		clientUpdateHub: newClientUpdateHub()}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/client/auth/login", server.clientLogin)
	client := router.Group("/api/v1/client")
	client.Use(server.requireClientBearer())
	client.POST("/sessions", server.clientCreateSession)
	client.GET("/sessions/:id/messages", server.clientListMessages)
	client.POST("/sessions/:id/messages", server.clientCreateMessage)
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
	node, _, err := server.nodes.Create(ctx, "client-session-node", []string{"discord"}, 8)
	require.NoError(t, err)
	repositoryID, _, profileID := seedWorkerGitHubQueue(t, db, 8801)
	environmentID, forumID := seedDevelopmentOperation(t, db, repositoryID, node.ID)
	projectID := developmentProjectIDForForum(t, db, forumID)
	var sessionID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO development_sessions(
		development_environment_id,development_project_id,agent_profile_id,title)
		VALUES ($1,$2,$3,'Concurrent clients') RETURNING id`, environmentID, projectID,
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
					SourceType: codexcontrol.SourceDevelopment, SessionID: sessionID,
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
