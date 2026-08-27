package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

type desktopThreadTarget struct {
	projectID     uuid.UUID
	forumID       uuid.UUID
	forumDiscord  string
	repository    string
	workspacePath string
	sourceControl uuid.UUID
	actorID       string
	actorName     string
}

func (s *Server) workerPrepareDesktopThread(c *gin.Context) {
	var request workerprotocol.DesktopThreadPrepareRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	if request.WorkspaceID == uuid.Nil || (request.Operation != "start" && request.Operation != "fork") ||
		!validDesktopRequestKey(request.RequestKey) {
		badRequest(c, errors.New("desktop thread reservation 参数无效"))
		return
	}
	params, target, err := s.desktopThreadTarget(c, request)
	if err != nil {
		problem(c, http.StatusForbidden, "Desktop Thread 不属于当前Workspace", err)
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建 Desktop Thread reservation 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var requestID uuid.UUID
	err = tx.QueryRowContext(c.Request.Context(), `SELECT id FROM desktop_thread_requests
			WHERE workspace_id = $1 AND request_key = $2
			AND status NOT IN ('failed') FOR UPDATE`, request.WorkspaceID,
		request.RequestKey).Scan(&requestID)
	if errors.Is(err, sql.ErrNoRows) {
		requestID = uuid.New()
		_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO desktop_thread_requests
				(id, workspace_id, workspace_project_id, operation, request_key,
				 source_control_id, cwd, request_params, status, forum_id)
				VALUES ($1,$2,$3,$4,$5,NULLIF($6::text,'')::uuid,$7,$8,'preparing',
				 NULLIF($9::text,'')::uuid)`,
			requestID, request.WorkspaceID, target.projectID, request.Operation, request.RequestKey,
			nilUUIDString(target.sourceControl), target.workspacePath, params,
			nilUUIDString(target.forumID))
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "创建 Desktop Thread reservation 失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交 Desktop Thread reservation 失败", err)
		return
	}
	state, err := s.loadDesktopThreadState(c, requestID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Desktop Thread reservation 失败", err)
		return
	}
	c.JSON(http.StatusOK, state)
}

func (s *Server) desktopThreadTarget(c *gin.Context,
	request workerprotocol.DesktopThreadPrepareRequest,
) (json.RawMessage, desktopThreadTarget, error) {
	var params map[string]any
	if json.Unmarshal(request.Params, &params) != nil {
		return nil, desktopThreadTarget{}, errors.New("desktop thread 参数不是 JSON 对象")
	}
	if ephemeral, _ := params["ephemeral"].(bool); ephemeral {
		return nil, desktopThreadTarget{}, errors.New("不支持 ephemeral Thread")
	}
	if forkPath, _ := params["path"].(string); strings.TrimSpace(forkPath) != "" {
		return nil, desktopThreadTarget{}, errors.New("不支持 path-based fork")
	}
	workspaceRoot := "/var/lib/tyrs-hand"
	if strings.TrimSpace(request.WorkspaceRoot) != "" {
		workspaceRoot = path.Clean(strings.TrimSpace(request.WorkspaceRoot))
		if !path.IsAbs(workspaceRoot) {
			return nil, desktopThreadTarget{}, errors.New("宿主工作区根目录必须是绝对路径")
		}
	}
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT project.id,
		f.id::text, COALESCE(r.discord_id,''), project.name, project.relative_path,
		COALESCE(project.project_source,'workspace_child'), COALESCE(project.host_path,''),
		COALESCE(e.owner_discord_user_id, ''),
		COALESCE(NULLIF(m.display_name, ''), m.username, '')
		FROM worker_workspaces e
		JOIN workspace_projects project ON project.workspace_id=e.id
		LEFT JOIN discord_forums f ON f.workspace_project_id=project.id
			AND f.workspace_id=e.id AND f.binding_status='active'
		LEFT JOIN discord_resources r ON r.id=f.resource_id
		LEFT JOIN discord_members m ON m.guild_id = e.guild_id
			AND m.discord_user_id = e.owner_discord_user_id
		WHERE e.id = $1 AND e.worker_id = $2
		AND project.availability_status='available'`,
		request.WorkspaceID, currentWorker(c).ID)
	if err != nil {
		return nil, desktopThreadTarget{}, err
	}
	defer func() { _ = rows.Close() }()
	var targets []desktopThreadTarget
	for rows.Next() {
		var target desktopThreadTarget
		var forumID sql.NullString
		var relative, source, hostPath string
		if err := rows.Scan(&target.projectID, &forumID, &target.forumDiscord,
			&target.repository, &relative, &source, &hostPath, &target.actorID, &target.actorName); err != nil {
			return nil, desktopThreadTarget{}, err
		}
		target.forumID = parseOptionalUUID(forumID)
		target.workspacePath, err = desktopWorkspacePath(workspaceRoot, relative, source, hostPath)
		if err != nil {
			return nil, desktopThreadTarget{}, err
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, desktopThreadTarget{}, err
	}
	if request.Operation == "fork" {
		sourceThread, _ := params["threadId"].(string)
		if sourceThread == "" {
			return nil, desktopThreadTarget{}, errors.New("fork 缺少源 Thread")
		}
		var sourceProject, sourceEnvironment, sourceControl uuid.UUID
		err := s.db.QueryRowContext(c.Request.Context(), `SELECT control.id,
			control.workspace_id,
			control.workspace_project_id
			FROM codex_thread_controls control
			WHERE control.external_thread_id = $1`, sourceThread).
			Scan(&sourceControl, &sourceEnvironment, &sourceProject)
		if err != nil || sourceEnvironment != request.WorkspaceID {
			return nil, desktopThreadTarget{}, errors.New("fork 源 Thread 未绑定到相同 Workspace 项目")
		}
		for _, target := range targets {
			if target.projectID == sourceProject {
				target.sourceControl = sourceControl
				params["cwd"] = target.workspacePath
				normalized, marshalErr := json.Marshal(params)
				return normalized, target, marshalErr
			}
		}
		return nil, desktopThreadTarget{}, errors.New("fork 源 Thread 的 Workspace 项目已不存在")
	}
	cwd, _ := params["cwd"].(string)
	cwd = path.Clean(strings.TrimSpace(cwd))
	if !path.IsAbs(cwd) {
		return nil, desktopThreadTarget{}, errors.New("cwd 必须是绝对路径")
	}
	var matched *desktopThreadTarget
	for index := range targets {
		target := &targets[index]
		if cwd != target.workspacePath && !strings.HasPrefix(cwd, target.workspacePath+"/") {
			continue
		}
		if matched == nil || len(target.workspacePath) > len(matched.workspacePath) {
			matched = target
		}
	}
	if matched == nil {
		return nil, desktopThreadTarget{}, errors.New("cwd 没有匹配本环境的 Workspace 项目")
	}
	target := *matched
	params["cwd"] = target.workspacePath
	normalized, err := json.Marshal(params)
	return normalized, target, err
}

func desktopWorkspacePath(root, relative string, extras ...string) (string, error) {
	source, hostPath := "workspace_child", ""
	if len(extras) > 0 {
		source = extras[0]
	}
	if len(extras) > 1 {
		hostPath = extras[1]
	}
	if hostPath != "" {
		if !filepath.IsAbs(hostPath) {
			return "", errors.New("workspace 项目主机路径必须是绝对路径")
		}
		return filepath.Clean(hostPath), nil
	}
	clean := path.Clean(strings.TrimSpace(relative))
	parts := strings.Split(clean, "/")
	if source == "workspace_root" && clean == "workspaces" {
		return filepath.Clean(root), nil
	}
	if len(parts) != 2 || parts[0] != "workspaces" || parts[1] == "" ||
		parts[1] == "." || parts[1] == ".." {
		return "", errors.New("workspace 项目路径必须是 workspaces/<name>")
	}
	return path.Join(root, parts[1]), nil
}

func enqueueDesktopThreadPost(ctx context.Context, tx *sql.Tx, requestID uuid.UUID,
	target desktopThreadTarget, title, input string,
) error {
	actor := target.actorName
	if actor == "" {
		actor = "Desktop"
	}
	input = desktopProjectionText(input)
	name := normalizeDesktopTitle(desktopProjectionText(title))
	if name == "" {
		name = actor + " · Desktop"
	}
	if len([]rune(name)) > 100 {
		name = string([]rune(name)[:100])
	}
	card := discordintegration.DesktopInputCards(actor, input)[0]
	payload, _ := json.Marshal(map[string]any{"channelId": target.forumDiscord,
		"threadName": name, "card": card, "desktopThreadRequestId": requestID.String()})
	return discordintegration.EnqueueTx(ctx, tx, "desktop-thread-post:"+requestID.String(),
		"forum.post.create", "channels/"+target.forumDiscord+"/threads", json.RawMessage(payload),
		"desktop-thread-"+requestID.String())
}

func normalizeDesktopTitle(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 100 {
		value = string(runes[:100])
	}
	return strings.TrimSpace(value)
}

func validDesktopRequestKey(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func nilUUIDString(value uuid.UUID) string {
	if value == uuid.Nil {
		return ""
	}
	return value.String()
}

func (s *Server) workerDesktopThreadState(c *gin.Context) {
	requestID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	state, err := s.loadDesktopThreadState(c, requestID)
	if errors.Is(err, sql.ErrNoRows) {
		problem(c, http.StatusNotFound, "Desktop Thread reservation 不存在", err)
		return
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Desktop Thread reservation 失败", err)
		return
	}
	c.JSON(http.StatusOK, state)
}

func (s *Server) workerCompleteDesktopThread(c *gin.Context) {
	requestID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	var request workerprotocol.DesktopThreadCompleteRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	threadID, err := desktopThreadID(request.Response)
	if err != nil || request.WorkspaceID == uuid.Nil {
		badRequest(c, errors.New("codex thread 完成结果无效"))
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "绑定 Desktop Thread 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	var workspaceID, controlID, workerID uuid.UUID
	var forumID sql.NullString
	var sourceControl, projectID sql.NullString
	err = tx.QueryRowContext(c.Request.Context(), `SELECT r.workspace_id, r.status,
		r.forum_id::text, r.source_control_id::text, r.workspace_project_id::text,
		e.worker_id
			FROM desktop_thread_requests r JOIN worker_workspaces e
			ON e.id = r.workspace_id
			JOIN workspace_projects project ON project.id=r.workspace_project_id
			LEFT JOIN discord_forums f ON f.id=r.forum_id
			WHERE r.id = $1 AND e.worker_id = $2
			AND (r.forum_id IS NULL OR f.binding_status='active')
			AND project.availability_status='available' FOR UPDATE OF r`,
		requestID, currentWorker(c).ID).Scan(&workspaceID, &status, &forumID,
		&sourceControl, &projectID, &workerID)
	if err != nil {
		problem(c, http.StatusNotFound, "Desktop Thread reservation 不存在", err)
		return
	}
	if workspaceID != request.WorkspaceID {
		problem(c, http.StatusForbidden, "Desktop Thread 不属于当前Workspace", nil)
		return
	}
	if status == "preparing" {
		profileID, model, effort, tier, configErr :=
			s.desktopControlConfig(c.Request.Context(), sourceControl, request.Response)
		if configErr != nil {
			problem(c, http.StatusInternalServerError, "解析 Desktop Thread 运行配置失败", configErr)
			return
		}
		controlID = uuid.New()
		sessionID := uuid.New()
		_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO workspace_sessions(
			id,workspace_id,workspace_project_id,agent_profile_id,title,
			model,reasoning_effort,service_tier)
			VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),
				COALESCE(NULLIF($8,''),'standard'))`, sessionID,
			workspaceID, projectID.String, profileID, threadID, model, effort, tier)
		if err == nil {
			_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO codex_thread_controls
			(id, source_type, session_id, workspace_project_id, agent_profile_id, external_thread_id,
			 worker_id, workspace_id, model, reasoning_effort, service_tier,
			 runtime_preferences_frozen_at)
			VALUES ($1,'workspace_session',$2,$3,$4,$5,$6,$7,
				NULLIF($8,''),NULLIF($9,''),NULLIF($10,''),now())`, controlID,
				sessionID, projectID.String, profileID, threadID,
				workerID, workspaceID, model, effort, tier)
		}
		if err == nil {
			_, err = tx.ExecContext(c.Request.Context(), `UPDATE desktop_thread_requests SET
				status = 'waiting_for_input', control_id = $2, external_thread_id = $3,
				response = $4, error = NULL, updated_at = now() WHERE id = $1`,
				requestID, controlID, threadID, request.Response)
		}
		if err != nil {
			problem(c, http.StatusConflict, "绑定 Desktop Thread Control 失败", err)
			return
		}
	} else if status != "waiting_for_input" && status != "post_pending" &&
		status != "completed" && status != "post_failed" {
		problem(c, http.StatusConflict, "Desktop Thread 尚未准备好或已经失败", nil)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交 Desktop Thread 绑定失败", err)
		return
	}
	state, err := s.loadDesktopThreadState(c, requestID)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Desktop Thread 结果失败", err)
		return
	}
	c.JSON(http.StatusOK, state)
}

func (s *Server) desktopControlConfig(ctx context.Context, sourceControl sql.NullString,
	response json.RawMessage,
) (uuid.UUID, string, string, string, error) {
	var profileID uuid.UUID
	if sourceControl.Valid {
		err := s.db.QueryRowContext(ctx, `SELECT agent_profile_id
			FROM codex_thread_controls WHERE id = $1`, sourceControl.String).Scan(&profileID)
		if err != nil {
			return uuid.Nil, "", "", "", err
		}
	} else {
		err := s.db.QueryRowContext(ctx, `SELECT id FROM agent_profiles
			ORDER BY created_at, id LIMIT 1`).Scan(&profileID)
		if err != nil {
			return uuid.Nil, "", "", "", err
		}
	}
	config := desktopRuntimeFromResponse(response)
	return profileID, config.Model, config.ReasoningEffort, config.ServiceTier, nil
}

func (s *Server) workerFailDesktopThread(c *gin.Context) {
	requestID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		badRequest(c, err)
		return
	}
	var request workerprotocol.DesktopThreadFailRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	request.Error = strings.TrimSpace(request.Error)
	if request.WorkspaceID == uuid.Nil || request.Error == "" {
		badRequest(c, errors.New("desktop thread 失败结果无效"))
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "保存 Desktop Thread 失败状态失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var workspaceID uuid.UUID
	var status, threadID, messageID string
	err = tx.QueryRowContext(c.Request.Context(), `SELECT r.workspace_id, r.status,
		COALESCE(c.thread_id,''), COALESCE(c.starter_message_id,'')
		FROM desktop_thread_requests r JOIN worker_workspaces e ON e.id = r.workspace_id
		LEFT JOIN discord_conversations c ON c.id = r.conversation_id
		WHERE r.id = $1 AND e.worker_id = $2 FOR UPDATE`, requestID, currentWorker(c).ID).
		Scan(&workspaceID, &status, &threadID, &messageID)
	if err != nil {
		problem(c, http.StatusNotFound, "Desktop Thread reservation 不存在", err)
		return
	}
	if workspaceID != request.WorkspaceID {
		problem(c, http.StatusForbidden, "Desktop Thread 不属于当前Workspace", nil)
		return
	}
	if status == "completed" {
		problem(c, http.StatusConflict, "Desktop Thread 已经完成", nil)
		return
	}
	_, err = tx.ExecContext(c.Request.Context(), `UPDATE desktop_thread_requests SET status = 'failed',
		error = $2, updated_at = now() WHERE id = $1 AND status <> 'failed'`, requestID, request.Error)
	if err == nil && threadID != "" && messageID != "" {
		err = enqueueDesktopThreadFailure(c, tx, requestID, threadID, messageID, request.Error)
	}
	if err != nil {
		problem(c, http.StatusInternalServerError, "保存 Desktop Thread 失败状态失败", err)
		return
	}
	if err := tx.Commit(); err != nil {
		problem(c, http.StatusInternalServerError, "提交 Desktop Thread 失败状态失败", err)
		return
	}
	c.Status(http.StatusNoContent)
}
