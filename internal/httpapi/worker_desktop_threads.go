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
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

type desktopThreadTarget struct {
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
	if request.EnvironmentID == uuid.Nil || (request.Operation != "start" && request.Operation != "fork") ||
		!validDesktopRequestKey(request.RequestKey) {
		badRequest(c, errors.New("desktop thread reservation 参数无效"))
		return
	}
	params, target, err := s.desktopThreadTarget(c, request)
	if err != nil {
		problem(c, http.StatusForbidden, "Desktop Thread 不属于当前开发环境", err)
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
			WHERE environment_id = $1 AND request_key = $2
			AND status NOT IN ('failed') FOR UPDATE`, request.EnvironmentID,
		request.RequestKey).Scan(&requestID)
	if errors.Is(err, sql.ErrNoRows) {
		requestID = uuid.New()
		_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO desktop_thread_requests
				(id, environment_id, operation, request_key, source_control_id, cwd,
				 request_params, status, forum_id)
				VALUES ($1,$2,$3,$4,NULLIF($5::text,'')::uuid,$6,$7,'preparing',$8)`,
			requestID, request.EnvironmentID, request.Operation, request.RequestKey,
			nilUUIDString(target.sourceControl), target.workspacePath, params, target.forumID)
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
	rows, err := s.db.QueryContext(c.Request.Context(), `SELECT f.id, r.discord_id,
		repo.owner || '/' || repo.name, fw.relative_path,
		COALESCE(e.ssh_discord_user_id, ''),
		COALESCE(NULLIF(m.display_name, ''), m.username, '')
		FROM discord_development_environments e
		JOIN discord_forums f ON f.development_environment_id = e.id
		JOIN discord_resources r ON r.id = f.resource_id
		JOIN repositories repo ON repo.id = f.repository_id
		JOIN discord_forum_workspaces fw ON fw.forum_id = f.id
		LEFT JOIN discord_members m ON m.guild_id = e.guild_id
			AND m.discord_user_id = e.ssh_discord_user_id
		WHERE e.id = $1 AND e.execution_node_id = $2 AND e.status NOT IN ('deleting','error')`,
		request.EnvironmentID, workerNode(c).ID)
	if err != nil {
		return nil, desktopThreadTarget{}, err
	}
	defer func() { _ = rows.Close() }()
	var targets []desktopThreadTarget
	for rows.Next() {
		var target desktopThreadTarget
		var relative string
		if err := rows.Scan(&target.forumID, &target.forumDiscord, &target.repository,
			&relative, &target.actorID, &target.actorName); err != nil {
			return nil, desktopThreadTarget{}, err
		}
		target.workspacePath = path.Join("/var/lib/tyrs-hand", relative)
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
		var sourceForum, sourceEnvironment, sourceControl uuid.UUID
		err := s.db.QueryRowContext(c.Request.Context(), `SELECT control.id,
			control.development_environment_id,
			COALESCE(conversation.forum_id, request.forum_id)
			FROM codex_thread_controls control
			LEFT JOIN discord_conversations conversation
				ON conversation.id = control.discord_conversation_id
			LEFT JOIN desktop_thread_requests request ON request.control_id = control.id
			WHERE control.external_thread_id = $1`, sourceThread).
			Scan(&sourceControl, &sourceEnvironment, &sourceForum)
		if err != nil || sourceEnvironment != request.EnvironmentID {
			return nil, desktopThreadTarget{}, errors.New("fork 源 Thread 未绑定到相同 Development Forum")
		}
		for _, target := range targets {
			if target.forumID == sourceForum {
				target.sourceControl = sourceControl
				params["cwd"] = target.workspacePath
				normalized, marshalErr := json.Marshal(params)
				return normalized, target, marshalErr
			}
		}
		return nil, desktopThreadTarget{}, errors.New("fork 源 Thread 的 Development Forum 已不存在")
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
		return nil, desktopThreadTarget{}, errors.New("cwd 没有匹配本环境的 Development Forum")
	}
	target := *matched
	params["cwd"] = target.workspacePath
	normalized, err := json.Marshal(params)
	return normalized, target, err
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
	_, err := tx.ExecContext(ctx, `INSERT INTO integration_outbox
		(integration, operation_key, operation_type, route_key, payload, nonce)
		VALUES ('discord',$1,'forum.post.create',$2,$3,$4)
		ON CONFLICT(integration, operation_key) DO UPDATE SET
			operation_type = EXCLUDED.operation_type, route_key = EXCLUDED.route_key,
			payload = EXCLUDED.payload, nonce = EXCLUDED.nonce, status = 'pending',
			attempt_count = 0, available_at = now(), last_error = NULL, updated_at = now()`,
		"desktop-thread-post:"+requestID.String(), "channels/"+target.forumDiscord+"/threads",
		payload, "desktop-thread-"+requestID.String())
	return err
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
	if err != nil || request.EnvironmentID == uuid.Nil {
		badRequest(c, errors.New("codex thread 完成结果无效"))
		return
	}
	provider, err := s.settings.AgentProvider(c.Request.Context())
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取 Codex Provider 配置失败", err)
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "绑定 Desktop Thread 失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	var environmentID, controlID, forumID, repositoryID, executionNodeID uuid.UUID
	var sourceControl sql.NullString
	err = tx.QueryRowContext(c.Request.Context(), `SELECT r.environment_id, r.status,
		r.forum_id, r.source_control_id::text, f.repository_id, e.execution_node_id
			FROM desktop_thread_requests r JOIN discord_development_environments e
			ON e.id = r.environment_id JOIN discord_forums f ON f.id = r.forum_id
			WHERE r.id = $1 AND e.execution_node_id = $2 FOR UPDATE`,
		requestID, workerNode(c).ID).Scan(&environmentID, &status, &forumID,
		&sourceControl, &repositoryID, &executionNodeID)
	if err != nil {
		problem(c, http.StatusNotFound, "Desktop Thread reservation 不存在", err)
		return
	}
	if environmentID != request.EnvironmentID {
		problem(c, http.StatusForbidden, "Desktop Thread 不属于当前开发环境", nil)
		return
	}
	if status == "preparing" {
		profileID, contextVersion, model, effort, tier, configErr :=
			s.desktopControlConfig(c.Request.Context(), sourceControl, request.Response)
		if configErr != nil {
			problem(c, http.StatusInternalServerError, "解析 Desktop Thread 运行配置失败", configErr)
			return
		}
		controlID = uuid.New()
		_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO codex_thread_controls
			(id, source_type, repository_id, agent_profile_id, context_version, external_thread_id,
			 execution_node_id, development_environment_id, model, reasoning_effort, service_tier,
			 runtime_preferences_frozen_at, codex_home_key, provider_signature)
			VALUES ($1,'desktop_thread',$2,$3,$4,$5,$6,$7,NULLIF($8,''),NULLIF($9,''),NULLIF($10,''),
			 now(),$11,$12)`, controlID, repositoryID, profileID, contextVersion, threadID,
			executionNodeID, environmentID, model, effort, tier, environmentID.String(),
			provider.ConfigSignature)
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
) (uuid.UUID, int64, string, string, string, error) {
	var profileID uuid.UUID
	var contextVersion int64
	if sourceControl.Valid {
		err := s.db.QueryRowContext(ctx, `SELECT agent_profile_id, context_version
			FROM codex_thread_controls WHERE id = $1`, sourceControl.String).
			Scan(&profileID, &contextVersion)
		if err != nil {
			return uuid.Nil, 0, "", "", "", err
		}
	} else {
		err := s.db.QueryRowContext(ctx, `SELECT id, context_version FROM agent_profiles
			ORDER BY created_at, id LIMIT 1`).Scan(&profileID, &contextVersion)
		if err != nil {
			return uuid.Nil, 0, "", "", "", err
		}
	}
	config := desktopRuntimeFromResponse(response)
	return profileID, contextVersion, config.Model, config.ReasoningEffort, config.ServiceTier, nil
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
	if request.EnvironmentID == uuid.Nil || request.Error == "" {
		badRequest(c, errors.New("desktop thread 失败结果无效"))
		return
	}
	tx, err := s.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		problem(c, http.StatusInternalServerError, "保存 Desktop Thread 失败状态失败", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var environmentID uuid.UUID
	var status, threadID, messageID string
	err = tx.QueryRowContext(c.Request.Context(), `SELECT r.environment_id, r.status,
		COALESCE(c.thread_id,''), COALESCE(c.starter_message_id,'')
		FROM desktop_thread_requests r JOIN discord_development_environments e ON e.id = r.environment_id
		LEFT JOIN discord_conversations c ON c.id = r.conversation_id
		WHERE r.id = $1 AND e.execution_node_id = $2 FOR UPDATE`, requestID, workerNode(c).ID).
		Scan(&environmentID, &status, &threadID, &messageID)
	if err != nil {
		problem(c, http.StatusNotFound, "Desktop Thread reservation 不存在", err)
		return
	}
	if environmentID != request.EnvironmentID {
		problem(c, http.StatusForbidden, "Desktop Thread 不属于当前开发环境", nil)
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
