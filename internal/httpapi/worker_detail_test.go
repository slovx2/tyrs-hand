package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/workerregistry"
	"github.com/stretchr/testify/require"
)

func TestGetWorkerChecksAssignmentAndMissingWorker(t *testing.T) {
	tests := []struct {
		name       string
		role       string
		assigned   bool
		missing    bool
		wantStatus int
	}{
		{name: "管理员", role: "admin", wantStatus: http.StatusOK},
		{name: "已分配普通用户", role: "user", assigned: true, wantStatus: http.StatusOK},
		{name: "未分配普通用户", role: "user", wantStatus: http.StatusForbidden},
		{name: "不存在", role: "user", missing: true, wantStatus: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			workerID, userID := uuid.New(), uuid.New()
			workerQuery := mock.ExpectQuery("SELECT id, name, roles, enabled, max_concurrent_jobs").
				WithArgs(workerID)
			if test.missing {
				workerQuery.WillReturnRows(workerRows())
			} else {
				workerQuery.WillReturnRows(workerRows().AddRow(workerID, "worker-a",
					[]byte(`["discord"]`), true, 2, workerregistry.ProtocolVersion,
					"test", "online", nil, "", "", []byte(`{}`)))
			}
			if test.role != "admin" && !test.missing {
				mock.ExpectQuery("SELECT EXISTS").WithArgs(workerID, userID).
					WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(test.assigned))
			}

			server := &Server{db: db, workers: workerregistry.NewService(db)}
			recorder := requestWorkerHandler(server, auth.Session{
				AdministratorID: userID,
				Role:            test.role,
			}, "/workers/"+workerID.String(), server.getWorker)
			require.Equal(t, test.wantStatus, recorder.Code)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestGetWorkerWorkspaceReturnsNullForUnboundAssignedWorker(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	workerID, userID := uuid.New(), uuid.New()
	mock.ExpectQuery("SELECT id, name, roles, enabled, max_concurrent_jobs").
		WithArgs(workerID).
		WillReturnRows(workerRows().AddRow(workerID, "worker-a", []byte(`["discord"]`),
			true, 2, workerregistry.ProtocolVersion, "test", "online", nil, "", "", []byte(`{}`)))
	mock.ExpectQuery("SELECT EXISTS").WithArgs(workerID, userID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery("SELECT e.id, e.owner_discord_user_id").WithArgs(workerID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	server := &Server{
		db:      db,
		workers: workerregistry.NewService(db),
		discord: discordintegration.NewManager(db, nil),
	}
	recorder := requestWorkerHandler(server, auth.Session{
		AdministratorID: userID,
		Role:            "user",
	}, "/workers/"+workerID.String()+"/workspace", server.getWorkerWorkspace)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"workspace":null}`, recorder.Body.String())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestScanWorkerWorkspaceChecksAccessBindingAndConnection(t *testing.T) {
	tests := []struct {
		name       string
		role       string
		assigned   bool
		bound      bool
		wantStatus int
	}{
		{name: "未分配普通用户", role: "user", wantStatus: http.StatusForbidden},
		{name: "尚未绑定", role: "admin", assigned: true, wantStatus: http.StatusConflict},
		{name: "Worker 离线", role: "admin", assigned: true, bound: true, wantStatus: http.StatusBadGateway},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			workerID, userID := uuid.New(), uuid.New()
			mock.ExpectQuery("SELECT id, name, roles, enabled, max_concurrent_jobs").
				WithArgs(workerID).
				WillReturnRows(workerRows().AddRow(workerID, "worker-a",
					[]byte(`["discord"]`), true, 2, workerregistry.ProtocolVersion,
					"test", "online", nil, "", "", []byte(`{}`)))
			if test.role != "admin" {
				mock.ExpectQuery("SELECT EXISTS").WithArgs(workerID, userID).
					WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(test.assigned))
			}
			if test.assigned {
				workspaceQuery := mock.ExpectQuery("SELECT e.id, e.owner_discord_user_id").
					WithArgs(workerID)
				rows := sqlmock.NewRows([]string{"id", "owner_discord_user_id",
					"owner_name", "worker_id", "projects_scanned_at", "project_scan_error"})
				if test.bound {
					rows.AddRow(uuid.New(), "owner", "Owner", workerID.String(), nil, "")
				}
				workspaceQuery.WillReturnRows(rows)
				if test.bound {
					mock.ExpectQuery("SELECT project.id, project.name").
						WillReturnRows(sqlmock.NewRows([]string{"id", "name", "relative_path",
							"desired_relative_path", "project_source", "host_path", "project_kind",
							"availability_status", "branch", "head_sha", "dirty", "remote_url",
							"last_seen_at", "scan_error", "forum_id", "forum_name", "discord_id",
							"binding_status"}))
				}
			}

			server := &Server{db: db, workers: workerregistry.NewService(db),
				discord:        discordintegration.NewManager(db, nil),
				workerRPCConns: make(map[uuid.UUID]*workerRPCConnection)}
			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.POST("/workers/:id/workspace/scan", func(c *gin.Context) {
				c.Set("session", auth.Session{AdministratorID: userID, Role: test.role})
				server.scanWorkerWorkspace(c)
			})
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost,
				"/workers/"+workerID.String()+"/workspace/scan", nil)
			router.ServeHTTP(recorder, request)
			require.Equal(t, test.wantStatus, recorder.Code)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestAssignWorkerUserOnlyAcceptsOrdinaryUser(t *testing.T) {
	tests := []struct {
		name       string
		rows       int64
		wantStatus int
	}{
		{name: "普通用户", rows: 1, wantStatus: http.StatusNoContent},
		{name: "管理员或不存在用户", rows: 0, wantStatus: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			workerID, userID := uuid.New(), uuid.New()
			mock.ExpectExec("INSERT INTO worker_administrators").
				WithArgs(workerID, userID).
				WillReturnResult(sqlmock.NewResult(0, test.rows))
			server := &Server{db: db}
			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.PUT("/workers/:id/users/:userId", server.assignWorkerUser)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPut,
				"/workers/"+workerID.String()+"/users/"+userID.String(), nil)
			router.ServeHTTP(recorder, request)
			require.Equal(t, test.wantStatus, recorder.Code)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func workerRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "name", "roles", "enabled",
		"max_concurrent_jobs", "protocol_version", "worker_version", "status",
		"heartbeat_at", "last_error", "ssh_host_key_fingerprint", "metadata"})
}

func requestWorkerHandler(server *Server, session auth.Session, path string,
	handler gin.HandlerFunc,
) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/workers/:id", func(c *gin.Context) {
		c.Set("session", session)
		handler(c)
	})
	router.GET("/workers/:id/workspace", func(c *gin.Context) {
		c.Set("session", session)
		handler(c)
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	router.ServeHTTP(recorder, request)
	return recorder
}
