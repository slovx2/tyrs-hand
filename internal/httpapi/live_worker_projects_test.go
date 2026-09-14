package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestListClientLiveWorkerProjectsReturnsPathIdentity(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	workerID := uuid.New()
	deviceID := uuid.New()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS(
		SELECT 1 FROM client_device_workers WHERE device_id=$1 AND worker_id=$2)`)).
		WithArgs(deviceID, workerID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT project.id, project.name,
		project.relative_path, COALESCE(project.host_path,''), project.availability_status
		FROM workspace_projects project
		JOIN worker_workspaces workspace ON workspace.id=project.workspace_id
		WHERE workspace.worker_id=$1 AND project.availability_status='available'
		ORDER BY project.name, project.relative_path`)).
		WithArgs(workerID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "relative_path", "coalesce", "availability_status",
		}).AddRow(workerID, "App", "workspaces/App", "/workspace/App", "available"))

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest("GET", "/api/v1/client/live-workers/"+workerID.String()+"/projects", nil)
	context.Params = gin.Params{{Key: "workerId", Value: workerID.String()}}
	context.Set(clientDeviceContext, deviceID)

	(&Server{db: db}).listClientLiveWorkerProjects(context)

	require.Equal(t, 200, recorder.Code, recorder.Body.String())
	var body struct {
		Projects []struct {
			ID                 string `json:"id"`
			Name               string `json:"name"`
			RelativePath       string `json:"relativePath"`
			HostPath           string `json:"hostPath"`
			AvailabilityStatus string `json:"availabilityStatus"`
		} `json:"projects"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Len(t, body.Projects, 1)
	require.Equal(t, workerID.String(), body.Projects[0].ID)
	require.Equal(t, "/workspace/App", body.Projects[0].HostPath)
	require.Equal(t, "workspaces/App", body.Projects[0].RelativePath)
	require.Equal(t, "available", body.Projects[0].AvailabilityStatus)
	require.NoError(t, mock.ExpectationsWereMet())
}
