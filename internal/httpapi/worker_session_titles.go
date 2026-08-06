package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/security"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/slovx2/tyrs-hand/internal/workerregistry"
)

const sessionTitleMaxRunes = 36

func (s *Server) workerClaimSessionTitle(c *gin.Context) {
	worker := currentWorker(c)
	if worker.Status == "incompatible" {
		problem(c, http.StatusConflict, "Worker 协议版本不兼容，禁止领取标题任务", nil)
		return
	}
	if !workerregistry.HasRole(worker, "discord") {
		problem(c, http.StatusForbidden, "节点未授权 Workspace 标题任务", nil)
		return
	}
	leaseToken, err := security.RandomToken(32)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建标题任务 Lease 失败", err)
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "领取标题任务失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(c.Request.Context(), `WITH exhausted AS (
		UPDATE workspace_session_title_tasks task SET status='failed',lease_owner=NULL,
			lease_token_hash=NULL,lease_expires_at=NULL,last_error_code='lease_expired',
			updated_at=now(),completed_at=now()
		FROM worker_workspaces workspace
		WHERE task.workspace_id=workspace.id AND workspace.worker_id=$1
		  AND task.status='claimed' AND task.attempt_count>=3
		  AND task.lease_expires_at<now()
		RETURNING task.session_id,task.title_revision
	)
	UPDATE workspace_sessions session SET title_source='fallback',updated_at=now()
	FROM exhausted WHERE session.id=exhausted.session_id
	  AND session.title_revision=exhausted.title_revision
	  AND session.title_source='generating'`, worker.ID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "恢复标题任务 Lease 失败", err)
		return
	}
	var task workerprotocol.SessionTitleTask
	err = tx.QueryRowContext(c.Request.Context(), `SELECT task.id,task.session_id,
		task.workspace_id,task.first_message_text,task.title_revision,task.attempt_count+1
		FROM workspace_session_title_tasks task
		JOIN worker_workspaces workspace ON workspace.id=task.workspace_id
		WHERE workspace.worker_id=$1 AND task.attempt_count<3
		  AND task.next_attempt_at<=now()
		  AND (task.status='pending' OR
			(task.status='claimed' AND task.lease_expires_at<now()))
		ORDER BY task.created_at,task.id
		FOR UPDATE OF task SKIP LOCKED LIMIT 1`, worker.ID).Scan(&task.ID, &task.SessionID,
		&task.WorkspaceID, &task.FirstMessage, &task.TitleRevision, &task.Attempt)
	if errors.Is(err, sql.ErrNoRows) {
		if err = tx.Commit(); err != nil {
			problem(c, http.StatusInternalServerError, "提交标题任务恢复失败", err)
			return
		}
		c.JSON(http.StatusOK, workerprotocol.SessionTitleClaimResponse{})
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "领取标题任务失败", err)
		return
	}
	task.LeaseToken = leaseToken
	task.LeaseExpiresAt = time.Now().Add(s.cfg.LeaseDuration)
	result, err := tx.ExecContext(c.Request.Context(), `UPDATE workspace_session_title_tasks
		SET status='claimed',attempt_count=$2,lease_owner=$3,lease_token_hash=$4,
			lease_expires_at=$5,last_error_code=NULL,updated_at=now()
		WHERE id=$1`, task.ID, task.Attempt, worker.ID, security.Digest(leaseToken),
		task.LeaseExpiresAt)
	if err == nil {
		var changed int64
		changed, err = result.RowsAffected()
		if err == nil && changed != 1 {
			err = errors.New("标题任务认领状态已变化")
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "提交标题任务认领失败", err)
		return
	}
	c.JSON(http.StatusOK, workerprotocol.SessionTitleClaimResponse{Task: &task})
}

func (s *Server) workerCompleteSessionTitle(c *gin.Context) {
	taskID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	var request workerprotocol.SessionTitleCompleteRequest
	if err = c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	request.Title = normalizeSessionTitle(request.Title)
	if request.LeaseToken == "" || request.Title == "" {
		badRequest(c, errors.New("标题任务完成参数无效"))
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "完成标题任务失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var sessionID uuid.UUID
	var taskRevision, currentRevision int64
	var titleSource string
	err = tx.QueryRowContext(c.Request.Context(), `SELECT task.session_id,task.title_revision,
		session.title_revision,session.title_source
		FROM workspace_session_title_tasks task
		JOIN workspace_sessions session ON session.id=task.session_id
		JOIN worker_workspaces workspace ON workspace.id=task.workspace_id
		WHERE task.id=$1 AND workspace.worker_id=$2 AND task.status='claimed'
		  AND task.lease_owner=$2 AND task.lease_token_hash=$3
		  AND task.lease_expires_at>=now() FOR UPDATE OF task,session`, taskID,
		currentWorker(c).ID, security.Digest(request.LeaseToken)).
		Scan(&sessionID, &taskRevision, &currentRevision, &titleSource)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusConflict, "标题任务 Lease 已失效", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "校验标题任务 Lease 失败", err)
		return
	}
	if request.TitleRevision != taskRevision || currentRevision != taskRevision ||
		titleSource == "manual" {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE workspace_session_title_tasks SET
			status='completed',lease_owner=NULL,lease_token_hash=NULL,lease_expires_at=NULL,
			completed_at=now(),updated_at=now() WHERE id=$1`, taskID)
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			problem(c, http.StatusInternalServerError, "丢弃过期标题结果失败", err)
			return
		}
		c.Status(http.StatusNoContent)
		return
	}
	updated, err := scanClientSession(tx.QueryRowContext(c.Request.Context(), `UPDATE workspace_sessions
		SET title=$2,generated_title=$2,title_source='generated',title_revision=title_revision+1,
			updated_at=now() WHERE id=$1 RETURNING `+clientSessionColumns, sessionID, request.Title))
	if err == nil {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE workspace_session_title_tasks SET
			status='completed',lease_owner=NULL,lease_token_hash=NULL,lease_expires_at=NULL,
			completed_at=now(),updated_at=now() WHERE id=$1`, taskID)
	}
	if err == nil {
		err = s.applySessionTitle(c, tx, sessionID, request.Title, true)
	}
	if err == nil {
		version := updated.SettingsVersion
		_, err = insertClientUpdate(c.Request.Context(), tx, &sessionID, "session.updated",
			"session", sessionID.String(), nil, &version, updated)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "提交生成标题失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) applySessionTitle(c *gin.Context, tx *sql.Tx, sessionID uuid.UUID,
	title string, generated bool,
) error {
	var controlID uuid.UUID
	var conversationID sql.NullString
	var revision int64
	err := tx.QueryRowContext(c.Request.Context(), `UPDATE codex_thread_controls SET
		desired_thread_name=$2,desired_thread_name_source='fallback',
		desired_thread_name_revision=desired_thread_name_revision+1,
		thread_name_last_error=NULL,updated_at=now()
		WHERE session_id=$1
		RETURNING id,discord_conversation_id::text,desired_thread_name_revision`,
		sessionID, title).Scan(&controlID, &conversationID, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil || !conversationID.Valid {
		return err
	}
	var threadID string
	err = tx.QueryRowContext(c.Request.Context(), `UPDATE discord_conversations SET
		title=$2,generated_title=CASE WHEN $3 THEN $2 ELSE NULL END,
		title_rename_status='completed',updated_at=now()
		WHERE id=$1 RETURNING thread_id`, conversationID.String, title, generated).Scan(&threadID)
	if err != nil {
		return err
	}
	return discordintegration.EnqueueThreadName(c.Request.Context(), tx, controlID,
		threadID, title, revision)
}

func (s *Server) workerFailSessionTitle(c *gin.Context) {
	taskID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	var request workerprotocol.SessionTitleFailRequest
	if err = c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	request.ErrorCode = strings.TrimSpace(request.ErrorCode)
	if request.LeaseToken == "" || request.ErrorCode == "" || len(request.ErrorCode) > 80 {
		badRequest(c, errors.New("标题任务失败参数无效"))
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "更新标题任务失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var sessionID uuid.UUID
	var revision int64
	var terminal bool
	err = tx.QueryRowContext(c.Request.Context(), `UPDATE workspace_session_title_tasks task SET
		status=CASE WHEN task.attempt_count>=3 THEN 'failed' ELSE 'pending' END,
		next_attempt_at=now()+CASE WHEN task.attempt_count<=1 THEN interval '5 seconds'
			ELSE interval '30 seconds' END,
		lease_owner=NULL,lease_token_hash=NULL,lease_expires_at=NULL,last_error_code=$4,
		completed_at=CASE WHEN task.attempt_count>=3 THEN now() ELSE NULL END,updated_at=now()
		FROM worker_workspaces workspace
		WHERE task.id=$1 AND task.workspace_id=workspace.id AND workspace.worker_id=$2
		  AND task.status='claimed' AND task.lease_owner=$2 AND task.lease_token_hash=$3
		  AND task.lease_expires_at>=now()
		RETURNING task.session_id,task.title_revision,task.attempt_count>=3`, taskID,
		currentWorker(c).ID, security.Digest(request.LeaseToken), request.ErrorCode).
		Scan(&sessionID, &revision, &terminal)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusConflict, "标题任务 Lease 已失效", err)
		return
	}
	if err == nil && terminal {
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE workspace_sessions SET
			title_source='fallback',updated_at=now()
			WHERE id=$1 AND title_revision=$2 AND title_source='generating'`, sessionID, revision)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "提交标题任务失败状态失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func normalizeSessionTitle(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > sessionTitleMaxRunes {
		value = string(runes[:sessionTitleMaxRunes])
	}
	return strings.TrimSpace(value)
}
