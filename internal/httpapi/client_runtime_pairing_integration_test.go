//go:build integration

package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/security"
	"github.com/stretchr/testify/require"
)

func seedClientRuntime(t *testing.T, db *sql.DB, workerID uuid.UUID, engine, fingerprint string) {
	t.Helper()
	_, err := db.ExecContext(t.Context(), `INSERT INTO worker_runtimes(
 worker_id,engine,enabled,status,ssh_listen_address,ssh_host_key_fingerprint,protocol_version)
 VALUES ($1,$2,true,'running',':3333',$3,'0.147.0')
 ON CONFLICT(worker_id,engine) DO UPDATE SET ssh_host_key_fingerprint=EXCLUDED.ssh_host_key_fingerprint`,
		workerID, engine, fingerprint)
	require.NoError(t, err)
}

func TestClientRuntimePairingAndTaskIsolation(t *testing.T) {
	db := workerDatabase(t)
	require.NoError(t, database.Migrate(t.Context(), db))
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	authService := auth.NewService(db, box, "runtime-setup", "")
	_, err = authService.Setup(t.Context(), "runtime-setup", "runtime-admin", "test-password-123")
	require.NoError(t, err)
	var adminID uuid.UUID
	require.NoError(t, db.QueryRow(`SELECT id FROM administrators WHERE username='runtime-admin'`).Scan(&adminID))
	server, endpoint := clientDeviceIntegrationServer(t, db, authService, adminID)
	worker, _, err := server.workers.Create(t.Context(), "dual-worker", []string{"discord"}, 2)
	require.NoError(t, err)
	for _, engine := range []string{"codex", "claude-code"} {
		seedClientRuntime(t, db, worker.ID, engine, testWorkerFingerprint(uuid.New()))
	}
	deviceID := uuid.New()
	token := "tdv1." + deviceID.String() + ".runtime-device-secret"
	base := endpoint + "/api/v1/client/machines/" + worker.ID.String()
	approve := func(engine string) {
		t.Helper()
		pairingID, _ := createAndClaimRuntimePairing(t, endpoint, worker.ID, deviceID, token, engine)
		result := clientJSONRequest(t, http.MethodPost, endpoint+"/api/v1/client-device-pairings/"+pairingID+"/approve", "", nil)
		require.Equal(t, http.StatusOK, result.Code, result.Body.String())
		require.Contains(t, result.Body.String(), `"engine":"`+engine+`"`)
	}
	approve("claude-code")
	live := clientJSONRequest(t, http.MethodGet, endpoint+"/api/v1/client/live-workers/"+worker.ID.String()+"/projects", token, nil)
	require.Equal(t, http.StatusForbidden, live.Code, "Claude 授权不能获得 Codex Live 访问权")
	denied := clientJSONRequest(t, http.MethodGet, base+"/runtimes/codex/scheduled-tasks", token, nil)
	require.Equal(t, http.StatusNotFound, denied.Code)
	approve("codex")
	machines := clientJSONRequest(t, http.MethodGet, endpoint+"/api/v1/client/machines", token, nil)
	require.Equal(t, http.StatusOK, machines.Code)
	var paired struct {
		Items []clientMachine `json:"items"`
	}
	require.NoError(t, json.Unmarshal(machines.Body.Bytes(), &paired))
	require.Len(t, paired.Items, 2, "同 Worker 的两个入口不能合并")
	require.NotEqual(t, paired.Items[0].Engine, paired.Items[1].Engine)

	fixture := seedScheduledClaimWorkspace(t, db, worker.ID)
	var profileID uuid.UUID
	require.NoError(t, db.QueryRow(`SELECT agent_profile_id FROM workspace_sessions WHERE id=$1`, fixture.session).Scan(&profileID))
	tasks := map[string]uuid.UUID{}
	for _, engine := range []string{"codex", "claude-code"} {
		var taskID uuid.UUID
		require.NoError(t, db.QueryRow(`INSERT INTO scheduled_tasks(workspace_id,workspace_project_id,
   engine,kind,name,prompt,status,schedule_text,timezone,schedule_kind,next_run_at,agent_profile_id)
   VALUES ($1,$2,$3,'standalone','同名任务','test','active','DTSTART:20300815T010000Z','UTC',
   'wall_clock',now()+interval '1 day',$4) RETURNING id`, fixture.workspace, fixture.project, engine, profileID).Scan(&taskID))
		tasks[engine] = taskID
		_, err := db.Exec(`INSERT INTO scheduled_task_runs(scheduled_task_id,schedule_revision,trigger,
   trigger_key,scheduled_for,status,task_snapshot) VALUES ($1,1,'scheduled','same-key',now(),'queued','{}')`, taskID)
		require.NoError(t, err)
	}
	for _, engine := range []string{"codex", "claude-code"} {
		other := "codex"
		if engine == other {
			other = "claude-code"
		}
		path := base + "/runtimes/" + engine + "/scheduled-tasks"
		result := clientJSONRequest(t, http.MethodGet, path+"?limit=1", token, nil)
		require.Equal(t, http.StatusOK, result.Code, result.Body.String())
		var page struct {
			Items      []clientScheduledTask `json:"items"`
			NextCursor string                `json:"nextCursor"`
		}
		require.NoError(t, json.Unmarshal(result.Body.Bytes(), &page))
		require.Len(t, page.Items, 1)
		require.Equal(t, tasks[engine], page.Items[0].ID)
		require.Equal(t, engine, string(page.Items[0].Engine))
		for _, suffix := range []string{"", "/runs"} {
			own := clientJSONRequest(t, http.MethodGet, path+"/"+tasks[engine].String()+suffix, token, nil)
			require.Equal(t, http.StatusOK, own.Code, own.Body.String())
			foreign := clientJSONRequest(t, http.MethodGet, path+"/"+tasks[other].String()+suffix, token, nil)
			require.Equal(t, http.StatusNotFound, foreign.Code, foreign.Body.String())
		}
		foreignCursor := clientJSONRequest(t, http.MethodGet, base+"/runtimes/"+other+
			"/scheduled-tasks?cursor="+url.QueryEscape(page.NextCursor), token, nil)
		require.Equal(t, http.StatusBadRequest, foreignCursor.Code)
	}
	for _, engine := range []string{"", "unknown"} {
		rejected := clientJSONRequest(t, http.MethodPost, endpoint+"/api/v1/client-device-pairings", "",
			map[string]any{"workerId": worker.ID, "engine": engine})
		require.Equal(t, http.StatusBadRequest, rejected.Code)
	}
	oldPath := clientJSONRequest(t, http.MethodGet, base+"/scheduled-tasks", token, nil)
	require.Equal(t, http.StatusNotFound, oldPath.Code, "旧路径不能猜测引擎")
	revoked := clientJSONRequest(t, http.MethodDelete, base+"/runtimes/claude-code", token, nil)
	require.Equal(t, http.StatusNoContent, revoked.Code)
	codex := clientJSONRequest(t, http.MethodGet, base+"/runtimes/codex/scheduled-tasks", token, nil)
	require.Equal(t, http.StatusOK, codex.Code)
	claude := clientJSONRequest(t, http.MethodGet, base+"/runtimes/claude-code/scheduled-tasks", token, nil)
	require.Equal(t, http.StatusNotFound, claude.Code)

	// 二维码生成后 Host Key 变更，原授权确认必须失效。
	pairingID, _ := createAndClaimRuntimePairing(t, endpoint, worker.ID, deviceID, token, "claude-code")
	seedClientRuntime(t, db, worker.ID, "claude-code", testWorkerFingerprint(uuid.New()))
	stale := clientJSONRequest(t, http.MethodPost, endpoint+"/api/v1/client-device-pairings/"+pairingID+"/approve", "", nil)
	require.Equal(t, http.StatusConflict, stale.Code)
	_, err = db.Exec(`UPDATE client_device_pairings SET engine='codex' WHERE id=$1`, pairingID)
	require.Error(t, err, "确认前也不允许更换配对引擎")
}
