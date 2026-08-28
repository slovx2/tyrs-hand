package httpapi

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/auth"
)

func (s *Server) workerAllowed(ctx context.Context, session auth.Session, workerID uuid.UUID) (bool, error) {
	if session.Role == "admin" {
		return true, nil
	}
	var allowed bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worker_administrators WHERE worker_id=$1 AND administrator_id=$2)`, workerID, session.AdministratorID).Scan(&allowed)
	return allowed, err
}

func (s *Server) requireWorkerAccess(c *gin.Context, workerID uuid.UUID) bool {
	session := c.MustGet("session").(auth.Session)
	allowed, err := s.workerAllowed(c.Request.Context(), session, workerID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "检查 Worker 权限失败", err)
		return false
	}
	if !allowed {
		problem(c, http.StatusForbidden, "没有该 Worker 的权限", nil)
		return false
	}
	return true
}

func (s *Server) listUsers(c *gin.Context) {
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT id, username, role, enabled, created_at FROM administrators ORDER BY username`)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取用户失败", err)
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var id uuid.UUID
		var username, role string
		var enabled bool
		var createdAt interface{}
		if err := rows.Scan(&id, &username, &role, &enabled, &createdAt); err != nil {
			problem(c, http.StatusInternalServerError, "读取用户失败", err)
			return
		}
		items = append(items, gin.H{"id": id, "username": username, "role": role, "enabled": enabled, "createdAt": createdAt})
	}
	if err := rows.Err(); err != nil {
		problem(c, http.StatusInternalServerError, "读取用户失败", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) setUserEnabled(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	var request struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	result, err := s.db.ExecContext(c.Request.Context(), `UPDATE administrators SET enabled=$1, updated_at=now() WHERE id=$2`, request.Enabled, id)
	if err != nil {
		problem(c, http.StatusInternalServerError, "更新用户状态失败", err)
		return
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		problem(c, http.StatusNotFound, "用户不存在", sql.ErrNoRows)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) listWorkerUsers(c *gin.Context) {
	workerID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT a.id,a.username,a.role,a.enabled FROM administrators a JOIN worker_administrators wa ON wa.administrator_id=a.id WHERE wa.worker_id=$1 AND a.role='user' ORDER BY a.username`, workerID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Worker 用户失败", err)
		return
	}
	defer rows.Close()
	items := make([]gin.H, 0)
	for rows.Next() {
		var id uuid.UUID
		var username, role string
		var enabled bool
		if err := rows.Scan(&id, &username, &role, &enabled); err != nil {
			problem(c, http.StatusInternalServerError, "读取 Worker 用户失败", err)
			return
		}
		items = append(items, gin.H{"id": id, "username": username, "role": role, "enabled": enabled})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (s *Server) assignWorkerUser(c *gin.Context) {
	workerID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	userID, err := uuid.Parse(c.Param("userId"))
	if err != nil {
		badRequest(c, err)
		return
	}
	result, err := s.db.ExecContext(c.Request.Context(), `INSERT INTO worker_administrators(worker_id,administrator_id)
		SELECT $1,id FROM administrators WHERE id=$2 AND role='user'
		ON CONFLICT (worker_id,administrator_id) DO UPDATE SET worker_id=EXCLUDED.worker_id`, workerID, userID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "分配 Worker 用户失败", err)
		return
	}
	count, err := result.RowsAffected()
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Worker 用户分配结果失败", err)
		return
	}
	if count == 0 {
		problem(c, http.StatusNotFound, "普通用户不存在", nil)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) removeWorkerUser(c *gin.Context) {
	workerID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	userID, err := uuid.Parse(c.Param("userId"))
	if err != nil {
		badRequest(c, err)
		return
	}
	if _, err := s.db.ExecContext(c.Request.Context(), `DELETE FROM worker_administrators WHERE worker_id=$1 AND administrator_id=$2`, workerID, userID); err != nil {
		problem(c, http.StatusInternalServerError, "移除 Worker 用户失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}
