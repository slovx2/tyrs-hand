package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

// 只有完成数据库、查询及写入隔离的接口才能向 Claude 开放。
// 新接口默认关闭；全局 Worker 操作由单一调度器使用 Codex 客户端执行。
func workerRuntimeScopedPath(path string) bool {
	switch path {
	case "/worker/v1/runs/:id/interactive", "/worker/v1/interactive/:id", "/worker/v1/interactive/answer",
		"/worker/v1/runs/:id/tools/call":
		return true
	}
	for _, prefix := range []string{
		"/worker/v1/desktop-thread-requests",
		"/worker/v1/session-title-tasks",
		"/worker/v1/thread-metadata-events",
		"/worker/v1/thread-name-updates",
		"/worker/v1/thread-lifecycle-requests",
	} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func validateWorkerRuntimeScope(c *gin.Context) bool {
	engine := currentWorkerEngine(c)
	scoped := workerRuntimeScopedPath(c.FullPath())
	if workerRuntimeRequiresEngine(c.FullPath()) || engine != "" {
		if err := engine.Validate(); err != nil {
			problem(c, http.StatusBadRequest, "缺少或无效的 Worker 运行时引擎", err)
			c.Abort()
			return false
		}
	}
	if engine == runtimeidentity.Claude && !scoped {
		problem(c, http.StatusNotImplemented, "此 Control 接口尚未支持 Claude 运行时", nil)
		c.Abort()
		return false
	}
	return true
}

func workerRuntimeRequiresEngine(path string) bool {
	switch path {
	case "/worker/v1/identity", "/worker/v1/heartbeat", "/worker/v1/ssh-configuration",
		"/worker/v1/workspace", "/worker/v1/config/ws":
		return false
	default:
		return true
	}
}

func currentWorkerEngine(c *gin.Context) runtimeidentity.Engine {
	return runtimeidentity.Engine(c.GetHeader(workerprotocol.EngineHeader))
}

func (s *Server) requireRuntimeControl(c *gin.Context, controlID uuid.UUID) bool {
	var exists bool
	err := s.db.QueryRowContext(c.Request.Context(), `SELECT EXISTS(
		SELECT 1 FROM codex_thread_controls control JOIN worker_workspaces workspace
		ON workspace.id=control.workspace_id WHERE control.id=$1
		AND workspace.worker_id=$2 AND control.engine=$3)`,
		controlID, currentWorker(c).ID, currentWorkerEngine(c)).Scan(&exists)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取运行时会话失败", err)
		return false
	}
	if !exists {
		problem(c, http.StatusNotFound, "当前运行时没有此会话", nil)
		return false
	}
	return true
}

func (s *Server) requireRuntimeIntent(c *gin.Context, intentID uuid.UUID) bool {
	var controlID uuid.UUID
	err := s.db.QueryRowContext(c.Request.Context(), `SELECT control_id FROM codex_turn_intents WHERE id=$1`, intentID).Scan(&controlID)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "当前运行时没有此输入", nil)
		return false
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取输入运行时失败", err)
		return false
	}
	return s.requireRuntimeControl(c, controlID)
}
