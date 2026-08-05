//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/security"
	"github.com/stretchr/testify/require"
)

func TestClientArchiveEmptyDesktopSessionLocally(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	authService := auth.NewService(db, box, "client-lifecycle-token", "")
	setup, err := authService.Setup(ctx, "client-lifecycle-token", "lifecycle-admin",
		"test-password-123")
	require.NoError(t, err)

	server, endpoint := clientIntegrationServer(t, db, authService)
	worker, _, err := server.workers.Create(ctx, "client-lifecycle-worker", []string{"discord"}, 4)
	require.NoError(t, err)
	repositoryID, _, profileID := seedWorkerGitHubQueue(t, db, 9911)
	workspaceID, forumID := seedWorkerWorkspace(t, db, repositoryID, worker.ID)
	projectID := workspaceProjectIDForForum(t, db, forumID)

	code, err := totp.GenerateCode(setup.TOTPSecret, time.Now())
	require.NoError(t, err)
	login := clientJSONRequest(t, http.MethodPost, endpoint+"/api/v1/client/auth/login", "",
		map[string]any{"username": "lifecycle-admin", "password": "test-password-123", "totp": code})
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	var loginBody struct {
		AccessToken string `json:"accessToken"`
	}
	require.NoError(t, json.Unmarshal(login.Body.Bytes(), &loginBody))

	var emptySessionID, emptyControlID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_sessions(
		workspace_id,workspace_project_id,agent_profile_id,title)
		VALUES ($1,$2,$3,'Empty Desktop session') RETURNING id`, workspaceID, projectID,
		profileID).Scan(&emptySessionID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_thread_controls(
		source_type,session_id,workspace_project_id,agent_profile_id,worker_id,workspace_id,
		external_thread_id) VALUES ('workspace_session',$1,$2,$3,$4,$5,$6) RETURNING id`,
		emptySessionID, projectID, profileID, worker.ID, workspaceID,
		"empty-desktop-thread").Scan(&emptyControlID))
	_, err = db.ExecContext(ctx, `INSERT INTO desktop_thread_requests(
		id,workspace_id,operation,request_key,cwd,request_params,status,forum_id,control_id,
		external_thread_id) VALUES ($1,$2,'start',$3,'/workspace','{}','waiting_for_input',
		$4,$5,'empty-desktop-thread')`, uuid.New(), workspaceID, strings.Repeat("e", 64),
		forumID, emptyControlID)
	require.NoError(t, err)

	archive := clientJSONRequest(t, http.MethodPost, endpoint+"/api/v1/client/sessions/"+
		emptySessionID.String()+"/archive", loginBody.AccessToken, nil)
	require.Equal(t, http.StatusOK, archive.Code, archive.Body.String())
	var archiveResult clientLifecycleResult
	require.NoError(t, json.Unmarshal(archive.Body.Bytes(), &archiveResult))
	require.Equal(t, "completed", archiveResult.Status)
	require.Equal(t, int64(1), archiveResult.Revision)

	var sessionState, controlState, requestStatus, updateState string
	var revision int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lifecycle_state FROM workspace_sessions
		WHERE id=$1`, emptySessionID).Scan(&sessionState))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lifecycle_state,lifecycle_revision
		FROM codex_thread_controls WHERE id=$1`, emptyControlID).Scan(&controlState, &revision))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM codex_thread_lifecycle_requests
		WHERE id=$1`, archiveResult.ID).Scan(&requestStatus))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT payload->>'lifecycleState'
		FROM client_updates WHERE session_id=$1 AND update_type='session.lifecycle'
		ORDER BY cursor DESC LIMIT 1`, emptySessionID).Scan(&updateState))
	require.Equal(t, "archived", sessionState)
	require.Equal(t, "archived", controlState)
	require.Equal(t, int64(1), revision)
	require.Equal(t, "completed", requestStatus)
	require.Equal(t, "archived", updateState)

	restore := clientJSONRequest(t, http.MethodPost, endpoint+"/api/v1/client/sessions/"+
		emptySessionID.String()+"/restore", loginBody.AccessToken, nil)
	require.Equal(t, http.StatusOK, restore.Code, restore.Body.String())
	require.NoError(t, json.Unmarshal(restore.Body.Bytes(), &archiveResult))
	require.Equal(t, "completed", archiveResult.Status)
	require.Equal(t, int64(2), archiveResult.Revision)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lifecycle_state FROM workspace_sessions
		WHERE id=$1`, emptySessionID).Scan(&sessionState))
	require.Equal(t, "active", sessionState)

	var regularSessionID, regularControlID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO workspace_sessions(
		workspace_id,workspace_project_id,agent_profile_id,title,last_message_seq)
		VALUES ($1,$2,$3,'Regular session',1) RETURNING id`, workspaceID, projectID,
		profileID).Scan(&regularSessionID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO codex_thread_controls(
		source_type,session_id,workspace_project_id,agent_profile_id,worker_id,workspace_id,
		external_thread_id) VALUES ('workspace_session',$1,$2,$3,$4,$5,$6) RETURNING id`,
		regularSessionID, projectID, profileID, worker.ID, workspaceID,
		"regular-thread").Scan(&regularControlID))
	_, err = db.ExecContext(ctx, `INSERT INTO session_messages(
		session_id,seq,local_id,message_role,content)
		VALUES ($1,1,'regular-message','user','{}')`, regularSessionID)
	require.NoError(t, err)

	regularArchive := clientJSONRequest(t, http.MethodPost, endpoint+"/api/v1/client/sessions/"+
		regularSessionID.String()+"/archive", loginBody.AccessToken, nil)
	require.Equal(t, http.StatusAccepted, regularArchive.Code, regularArchive.Body.String())
	require.NoError(t, json.Unmarshal(regularArchive.Body.Bytes(), &archiveResult))
	require.Equal(t, "applying", archiveResult.Status)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lifecycle_state FROM workspace_sessions
		WHERE id=$1`, regularSessionID).Scan(&sessionState))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT lifecycle_state FROM codex_thread_controls
		WHERE id=$1`, regularControlID).Scan(&controlState))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM codex_thread_lifecycle_requests
		WHERE id=$1`, archiveResult.ID).Scan(&requestStatus))
	require.Equal(t, "archive_pending", sessionState)
	require.Equal(t, "archive_pending", controlState)
	require.Equal(t, "applying", requestStatus)
}
