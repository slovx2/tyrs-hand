//go:build integration

package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/security"
	"github.com/slovx2/tyrs-hand/internal/workerregistry"
	"github.com/stretchr/testify/require"
)

func TestClientDevicePairingApprovalAndRevocation(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	authService := auth.NewService(db, box, "device-setup-token", "")
	_, err = authService.Setup(ctx, "device-setup-token", "device-admin", "test-password-123")
	require.NoError(t, err)

	var administratorID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx,
		"SELECT id FROM administrators WHERE username='device-admin'").Scan(&administratorID))
	server, endpoint := clientDeviceIntegrationServer(t, db, authService, administratorID)
	workerA, _, err := server.workers.Create(ctx, "pairing-worker-a", []string{"discord"}, 2)
	require.NoError(t, err)
	workerB, _, err := server.workers.Create(ctx, "pairing-worker-b", []string{"discord"}, 2)
	require.NoError(t, err)
	for _, worker := range []workerregistry.Worker{workerA, workerB} {
		_, err = db.ExecContext(ctx, `UPDATE workers SET ssh_host_key_fingerprint=$2
			WHERE id=$1`, worker.ID, testWorkerFingerprint(worker.ID))
		require.NoError(t, err)
	}

	created := clientJSONRequest(t, http.MethodPost,
		endpoint+"/api/v1/client-device-pairings", "", map[string]any{"workerId": workerA.ID})
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var pairing struct {
		ID         uuid.UUID `json:"id"`
		PairingURI string    `json:"pairingUri"`
		QRDataURL  string    `json:"qrDataUrl"`
	}
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &pairing))
	require.NotEqual(t, uuid.Nil, pairing.ID)
	require.True(t, strings.HasPrefix(pairing.QRDataURL, "data:image/png;base64,"))

	pairingURL, err := url.Parse(pairing.PairingURI)
	require.NoError(t, err)
	require.Equal(t, "tyrshand", pairingURL.Scheme)
	require.Equal(t, "device-pair", pairingURL.Host)
	require.Equal(t, "3", pairingURL.Query().Get("v"))
	_, err = uuid.Parse(pairingURL.Query().Get("serverId"))
	require.NoError(t, err)
	require.Equal(t, pairing.ID.String(), pairingURL.Query().Get("pairingId"))
	require.Equal(t, workerA.ID.String(), pairingURL.Query().Get("workerId"))
	require.Equal(t, testWorkerFingerprint(workerA.ID),
		pairingURL.Query().Get("sshHostKeyFingerprint"))
	pairingSecret := pairingURL.Query().Get("secret")
	require.NotEmpty(t, pairingSecret)

	deviceID := uuid.New()
	deviceToken := "tdv1." + deviceID.String() + ".permanent-device-secret"
	claim := clientJSONRequest(t, http.MethodPost,
		endpoint+"/api/v1/client/device-pairings/"+pairing.ID.String()+"/claim", "",
		map[string]any{
			"pairingSecret":  pairingSecret,
			"deviceId":       deviceID,
			"name":           "Pixel E2E",
			"platform":       "android",
			"credentialHash": security.Digest(deviceToken),
		})
	require.Equal(t, http.StatusOK, claim.Code, claim.Body.String())
	var claimed struct {
		ClaimToken string `json:"claimToken"`
		Status     string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(claim.Body.Bytes(), &claimed))
	require.NotEmpty(t, claimed.ClaimToken)
	require.Equal(t, "waiting_confirmation", claimed.Status)
	secondClaim := clientJSONRequest(t, http.MethodPost,
		endpoint+"/api/v1/client/device-pairings/"+pairing.ID.String()+"/claim", "",
		map[string]any{
			"pairingSecret":  pairingSecret,
			"deviceId":       uuid.New(),
			"name":           "Second device",
			"platform":       "android",
			"credentialHash": security.Digest("second-device-token"),
		})
	require.Equal(t, http.StatusUnauthorized, secondClaim.Code)

	beforeApproval := clientJSONRequest(t, http.MethodGet,
		endpoint+"/api/v1/client/machines", deviceToken, nil)
	require.Equal(t, http.StatusUnauthorized, beforeApproval.Code)

	status := clientPairingStatusRequest(t,
		endpoint+"/api/v1/client/device-pairings/"+pairing.ID.String()+"/status",
		claimed.ClaimToken)
	require.Equal(t, http.StatusOK, status.Code)
	require.Contains(t, status.Body.String(), "waiting_confirmation")

	approved := clientJSONRequest(t, http.MethodPost,
		endpoint+"/api/v1/client-device-pairings/"+pairing.ID.String()+"/approve", "",
		map[string]any{})
	require.Equal(t, http.StatusOK, approved.Code, approved.Body.String())
	require.Contains(t, approved.Body.String(), "Pixel E2E")

	afterApproval := clientJSONRequest(t, http.MethodGet,
		endpoint+"/api/v1/client/machines", deviceToken, nil)
	require.Equal(t, http.StatusOK, afterApproval.Code, afterApproval.Body.String())
	require.Contains(t, afterApproval.Body.String(), workerA.ID.String())
	require.Contains(t, afterApproval.Body.String(), testWorkerFingerprint(workerA.ID))
	listed := clientJSONRequest(t, http.MethodGet, endpoint+"/api/v1/client-devices", "", nil)
	require.Equal(t, http.StatusOK, listed.Code)
	require.Contains(t, listed.Body.String(), "Pixel E2E")

	approvedStatus := clientPairingStatusRequest(t,
		endpoint+"/api/v1/client/device-pairings/"+pairing.ID.String()+"/status",
		claimed.ClaimToken)
	require.Equal(t, http.StatusOK, approvedStatus.Code)
	require.Contains(t, approvedStatus.Body.String(), "approved")

	secondPairing := createAndClaimClientPairing(t, endpoint, workerB.ID, deviceID, deviceToken)
	approved = clientJSONRequest(t, http.MethodPost,
		endpoint+"/api/v1/client-device-pairings/"+secondPairing+"/approve", "",
		map[string]any{})
	require.Equal(t, http.StatusOK, approved.Code, approved.Body.String())
	afterApproval = clientJSONRequest(t, http.MethodGet,
		endpoint+"/api/v1/client/machines", deviceToken, nil)
	require.Equal(t, http.StatusOK, afterApproval.Code, afterApproval.Body.String())
	require.Contains(t, afterApproval.Body.String(), workerA.ID.String())
	require.Contains(t, afterApproval.Body.String(), workerB.ID.String())

	revokedMachine := clientJSONRequest(t, http.MethodDelete,
		endpoint+"/api/v1/client/machines/"+workerA.ID.String(), deviceToken, nil)
	require.Equal(t, http.StatusNoContent, revokedMachine.Code, revokedMachine.Body.String())
	afterApproval = clientJSONRequest(t, http.MethodGet,
		endpoint+"/api/v1/client/machines", deviceToken, nil)
	require.NotContains(t, afterApproval.Body.String(), workerA.ID.String())
	require.Contains(t, afterApproval.Body.String(), workerB.ID.String())

	deleted := clientJSONRequest(t, http.MethodDelete,
		endpoint+"/api/v1/client-devices/"+deviceID.String(), "", nil)
	require.Equal(t, http.StatusNoContent, deleted.Code, deleted.Body.String())

	afterDeletion := clientJSONRequest(t, http.MethodGet,
		endpoint+"/api/v1/client/machines", deviceToken, nil)
	require.Equal(t, http.StatusUnauthorized, afterDeletion.Code)
}

func TestClientDevicePairingRejectsExpiredAndConcurrentApproval(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	authService := auth.NewService(db, box, "pairing-state-setup", "")
	_, err = authService.Setup(ctx, "pairing-state-setup", "pairing-state-admin",
		"test-password-123")
	require.NoError(t, err)
	var administratorID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM administrators
		WHERE username='pairing-state-admin'`).Scan(&administratorID))
	server, endpoint := clientDeviceIntegrationServer(t, db, authService, administratorID)
	worker, _, err := server.workers.Create(ctx, "pairing-state-worker", []string{"discord"}, 2)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE workers SET ssh_host_key_fingerprint=$2 WHERE id=$1`,
		worker.ID, testWorkerFingerprint(worker.ID))
	require.NoError(t, err)

	rejectedID, rejectedClaim := createAndClaimClientPairingWithToken(t, endpoint, worker.ID,
		uuid.New(), "tdv1."+uuid.NewString()+".rejected-device-secret")
	rejected := clientJSONRequest(t, http.MethodPost, endpoint+"/api/v1/client-device-pairings/"+
		rejectedID+"/reject", "", map[string]any{})
	require.Equal(t, http.StatusNoContent, rejected.Code, rejected.Body.String())
	rejectedStatus := clientPairingStatusRequest(t, endpoint+
		"/api/v1/client/device-pairings/"+rejectedID+"/status", rejectedClaim)
	require.Equal(t, http.StatusOK, rejectedStatus.Code)
	require.Contains(t, rejectedStatus.Body.String(), "rejected")
	rejectedApproval := clientJSONRequest(t, http.MethodPost,
		endpoint+"/api/v1/client-device-pairings/"+rejectedID+"/approve", "", map[string]any{})
	require.Equal(t, http.StatusConflict, rejectedApproval.Code)

	expiredID, expiredClaim := createAndClaimClientPairingWithToken(t, endpoint, worker.ID,
		uuid.New(), "tdv1."+uuid.NewString()+".expired-device-secret")
	_, err = db.ExecContext(ctx, `UPDATE client_device_pairings SET expires_at=now()-interval '1 minute'
		WHERE id=$1`, expiredID)
	require.NoError(t, err)
	expiredStatus := clientPairingStatusRequest(t, endpoint+
		"/api/v1/client/device-pairings/"+expiredID+"/status", expiredClaim)
	require.Equal(t, http.StatusOK, expiredStatus.Code)
	require.Contains(t, expiredStatus.Body.String(), "expired")
	expiredApproval := clientJSONRequest(t, http.MethodPost,
		endpoint+"/api/v1/client-device-pairings/"+expiredID+"/approve", "", map[string]any{})
	require.Equal(t, http.StatusConflict, expiredApproval.Code)

	concurrentDeviceID := uuid.New()
	concurrentID, _ := createAndClaimClientPairingWithToken(t, endpoint, worker.ID,
		concurrentDeviceID, "tdv1."+concurrentDeviceID.String()+".concurrent-device-secret")
	const attempts = 8
	statuses := make(chan int, attempts)
	var wait sync.WaitGroup
	for range attempts {
		wait.Add(1)
		go func() {
			defer wait.Done()
			request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost,
				endpoint+"/api/v1/client-device-pairings/"+concurrentID+"/approve",
				strings.NewReader("{}"))
			if requestErr != nil {
				statuses <- 0
				return
			}
			request.Header.Set("Content-Type", "application/json")
			response, responseErr := http.DefaultClient.Do(request)
			if responseErr != nil {
				statuses <- 0
				return
			}
			_ = response.Body.Close()
			statuses <- response.StatusCode
		}()
	}
	wait.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	require.Equal(t, 1, counts[http.StatusOK], counts)
	require.Equal(t, attempts-1, counts[http.StatusConflict], counts)
	var bindings int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM client_device_workers
		WHERE device_id=$1 AND worker_id=$2`, concurrentDeviceID, worker.ID).Scan(&bindings))
	require.Equal(t, 1, bindings)
}

func createAndClaimClientPairing(t *testing.T, endpoint string, workerID, deviceID uuid.UUID,
	deviceToken string,
) string {
	t.Helper()
	pairingID, _ := createAndClaimClientPairingWithToken(t, endpoint, workerID, deviceID,
		deviceToken)
	return pairingID
}

func createAndClaimClientPairingWithToken(t *testing.T, endpoint string,
	workerID, deviceID uuid.UUID, deviceToken string,
) (string, string) {
	t.Helper()
	created := clientJSONRequest(t, http.MethodPost,
		endpoint+"/api/v1/client-device-pairings", "", map[string]any{"workerId": workerID})
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var pairing struct {
		ID         uuid.UUID `json:"id"`
		PairingURI string    `json:"pairingUri"`
	}
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &pairing))
	parsed, err := url.Parse(pairing.PairingURI)
	require.NoError(t, err)
	claim := clientJSONRequest(t, http.MethodPost,
		endpoint+"/api/v1/client/device-pairings/"+pairing.ID.String()+"/claim", "",
		map[string]any{"pairingSecret": parsed.Query().Get("secret"), "deviceId": deviceID,
			"name": "Pixel E2E", "platform": "android",
			"credentialHash": security.Digest(deviceToken)})
	require.Equal(t, http.StatusOK, claim.Code, claim.Body.String())
	var claimed struct {
		ClaimToken string `json:"claimToken"`
	}
	require.NoError(t, json.Unmarshal(claim.Body.Bytes(), &claimed))
	require.NotEmpty(t, claimed.ClaimToken)
	return pairing.ID.String(), claimed.ClaimToken
}

func TestClientScheduledTasksAreMachineScopedAndPaginated(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	authService := auth.NewService(db, box, "scheduled-device-setup", "")
	_, err = authService.Setup(ctx, "scheduled-device-setup", "scheduled-device-admin",
		"test-password-123")
	require.NoError(t, err)
	var administratorID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM administrators
		WHERE username='scheduled-device-admin'`).Scan(&administratorID))
	server, endpoint := clientDeviceIntegrationServer(t, db, authService, administratorID)
	workerA, _, err := server.workers.Create(ctx, "task-machine-a", []string{"discord"}, 2)
	require.NoError(t, err)
	workerB, _, err := server.workers.Create(ctx, "task-machine-b", []string{"discord"}, 2)
	require.NoError(t, err)
	workerC, _, err := server.workers.Create(ctx, "task-machine-unbound", []string{"discord"}, 2)
	require.NoError(t, err)
	for _, worker := range []workerregistry.Worker{workerA, workerB, workerC} {
		_, err = db.ExecContext(ctx, `UPDATE workers SET ssh_host_key_fingerprint=$2
			WHERE id=$1`, worker.ID, testWorkerFingerprint(worker.ID))
		require.NoError(t, err)
	}

	deviceID := uuid.New()
	deviceToken := "tdv1." + deviceID.String() + ".scheduled-task-device-secret"
	for _, workerID := range []uuid.UUID{workerA.ID, workerB.ID} {
		pairingID := createAndClaimClientPairing(t, endpoint, workerID, deviceID, deviceToken)
		approved := clientJSONRequest(t, http.MethodPost,
			endpoint+"/api/v1/client-device-pairings/"+pairingID+"/approve", "",
			map[string]any{})
		require.Equal(t, http.StatusOK, approved.Code, approved.Body.String())
	}

	fixture := seedScheduledClaimWorkspace(t, db, workerA.ID)
	var profileID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `SELECT agent_profile_id
		FROM workspace_sessions WHERE id=$1`, fixture.session).Scan(&profileID))
	createdAt := time.Now().UTC().Add(-3 * time.Hour)
	insertTask := func(name, status string, offset time.Duration) uuid.UUID {
		t.Helper()
		var taskID uuid.UUID
		require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO scheduled_tasks(
			workspace_id,workspace_project_id,kind,name,prompt,status,schedule_text,timezone,
			schedule_kind,next_run_at,agent_profile_id,service_tier,created_at,updated_at,deleted_at)
			VALUES ($1,$2,'standalone',$3,$4,$5,'DTSTART:20300815T010000Z','UTC',
			'wall_clock',now()+interval '1 day',$6,'standard',$7::timestamptz,$7::timestamptz,
			CASE WHEN $5='deleted' THEN $7::timestamptz ELSE NULL END) RETURNING id`, fixture.workspace,
			fixture.project, name, "prompt "+name, status, profileID, createdAt.Add(offset)).
			Scan(&taskID))
		return taskID
	}
	taskA := insertTask("每天检查", "active", 2*time.Hour)
	_ = insertTask("每周汇总", "paused", time.Hour)
	deletedTask := insertTask("已删除任务", "deleted", 0)

	var controlID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_thread_controls(
		source_type,session_id,workspace_project_id,agent_profile_id,worker_id,workspace_id,
		external_thread_id) VALUES ('workspace_session',$1,$2,$3,$4,$5,'codex-thread-scheduled')
		RETURNING id`, fixture.session, fixture.project, profileID, workerA.ID,
		fixture.workspace).Scan(&controlID))
	var intentID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_turn_intents(
		control_id,sequence_no,source_type,agent_profile_id,idempotency_key,status,
		workspace_project_id,session_id) VALUES ($1,1,'workspace_session',$2,$3,'completed',$4,$5)
		RETURNING id`, controlID, profileID, "mobile-task-run-"+uuid.NewString(),
		fixture.project, fixture.session).Scan(&intentID))
	for index := 0; index < 2; index++ {
		var runIntent any
		if index == 1 {
			runIntent = intentID
		}
		_, err = db.ExecContext(ctx, `INSERT INTO scheduled_task_runs(
			scheduled_task_id,schedule_revision,trigger,trigger_key,scheduled_for,status,
			intent_id,session_id,task_snapshot,created_at,updated_at,finished_at)
			VALUES ($1,1,'scheduled',$2,$3,'succeeded',$4,$5,'{}',$3,$3,$3)`, taskA,
			"occurrence-"+strconv.Itoa(index), createdAt.Add(time.Duration(index+1)*time.Hour),
			runIntent, fixture.session)
		require.NoError(t, err)
	}

	list := clientJSONRequest(t, http.MethodGet, endpoint+"/api/v1/client/machines/"+
		workerA.ID.String()+"/scheduled-tasks?limit=1", deviceToken, nil)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	var firstPage struct {
		Items      []clientScheduledTask `json:"items"`
		NextCursor string                `json:"nextCursor"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &firstPage))
	require.Len(t, firstPage.Items, 1)
	require.NotEmpty(t, firstPage.NextCursor)
	require.NotEqual(t, "deleted", firstPage.Items[0].Status)
	secondPage := clientJSONRequest(t, http.MethodGet, endpoint+"/api/v1/client/machines/"+
		workerA.ID.String()+"/scheduled-tasks?limit=1&cursor="+
		url.QueryEscape(firstPage.NextCursor), deviceToken, nil)
	require.Equal(t, http.StatusOK, secondPage.Code, secondPage.Body.String())
	require.NotContains(t, secondPage.Body.String(), deletedTask.String())

	deleted := clientJSONRequest(t, http.MethodGet, endpoint+"/api/v1/client/machines/"+
		workerA.ID.String()+"/scheduled-tasks?status=deleted", deviceToken, nil)
	require.Equal(t, http.StatusOK, deleted.Code, deleted.Body.String())
	require.Contains(t, deleted.Body.String(), deletedTask.String())
	detail := clientJSONRequest(t, http.MethodGet, endpoint+"/api/v1/client/machines/"+
		workerA.ID.String()+"/scheduled-tasks/"+taskA.String(), deviceToken, nil)
	require.Equal(t, http.StatusOK, detail.Code, detail.Body.String())
	require.Contains(t, detail.Body.String(), "每天检查")
	runs := clientJSONRequest(t, http.MethodGet, endpoint+"/api/v1/client/machines/"+
		workerA.ID.String()+"/scheduled-tasks/"+taskA.String()+"/runs?limit=1", deviceToken, nil)
	require.Equal(t, http.StatusOK, runs.Code, runs.Body.String())
	require.Contains(t, runs.Body.String(), "codex-thread-scheduled")
	require.Contains(t, runs.Body.String(), "nextCursor")

	unauthorized := clientJSONRequest(t, http.MethodGet, endpoint+"/api/v1/client/machines/"+
		workerC.ID.String()+"/scheduled-tasks", deviceToken, nil)
	require.Equal(t, http.StatusNotFound, unauthorized.Code, unauthorized.Body.String())
	_, err = db.ExecContext(ctx, `UPDATE worker_workspaces SET worker_id=$2 WHERE id=$1`,
		fixture.workspace, workerB.ID)
	require.NoError(t, err)
	oldMachine := clientJSONRequest(t, http.MethodGet, endpoint+"/api/v1/client/machines/"+
		workerA.ID.String()+"/scheduled-tasks", deviceToken, nil)
	require.NotContains(t, oldMachine.Body.String(), taskA.String())
	newMachine := clientJSONRequest(t, http.MethodGet, endpoint+"/api/v1/client/machines/"+
		workerB.ID.String()+"/scheduled-tasks", deviceToken, nil)
	require.Contains(t, newMachine.Body.String(), taskA.String())
}

func clientDeviceIntegrationServer(t *testing.T, db *sql.DB, authService *auth.Service,
	administratorID uuid.UUID,
) (*Server, string) {
	t.Helper()
	server := &Server{cfg: config.Config{LeaseDuration: time.Minute, PublicURL: "http://127.0.0.1"},
		db: db, auth: authService, workers: workerregistry.NewService(db),
		clientUpdateHub: newClientUpdateHub()}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	administrator := router.Group("/api/v1")
	administrator.Use(func(c *gin.Context) {
		c.Set("session", auth.Session{AdministratorID: administratorID, Username: "device-admin"})
		c.Next()
	})
	administrator.GET("/client-devices", server.listClientDevices)
	administrator.POST("/client-device-pairings", server.createClientDevicePairing)
	administrator.GET("/client-device-pairings/:id", server.getClientDevicePairing)
	administrator.POST("/client-device-pairings/:id/approve", server.approveClientDevicePairing)
	administrator.POST("/client-device-pairings/:id/reject", server.rejectClientDevicePairing)
	administrator.DELETE("/client-devices/:id", server.deleteClientDevice)
	router.POST("/api/v1/client/device-pairings/:id/claim", server.claimClientDevicePairing)
	router.GET("/api/v1/client/device-pairings/:id/status", server.clientDevicePairingStatus)
	client := router.Group("/api/v1/client")
	client.Use(server.requireClientBearer())
	client.GET("/machines", server.listClientMachines)
	client.DELETE("/machines/:workerId", server.deleteClientMachine)
	client.GET("/machines/:workerId/scheduled-tasks", server.listClientMachineScheduledTasks)
	client.GET("/machines/:workerId/scheduled-tasks/:taskId", server.getClientMachineScheduledTask)
	client.GET("/machines/:workerId/scheduled-tasks/:taskId/runs",
		server.listClientMachineScheduledTaskRuns)
	httpServer := httptest.NewServer(router)
	t.Cleanup(httpServer.Close)
	return server, httpServer.URL
}

func clientPairingStatusRequest(t *testing.T, endpoint, claimToken string) *httptest.ResponseRecorder {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Pairing "+claimToken)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	recorder := httptest.NewRecorder()
	recorder.Code = response.StatusCode
	_, err = io.Copy(recorder.Body, response.Body)
	require.NoError(t, err)
	return recorder
}
