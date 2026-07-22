package httpapi

import (
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
		AND status IN ('preparing','post_pending','codex_pending') FOR UPDATE`, request.EnvironmentID,
		request.RequestKey).Scan(&requestID)
	if errors.Is(err, sql.ErrNoRows) {
		requestID = uuid.New()
		_, err = tx.ExecContext(c.Request.Context(), `INSERT INTO desktop_thread_requests
			(id, environment_id, operation, request_key, source_control_id, cwd,
			 request_params, status, forum_id)
			VALUES ($1,$2,$3,$4,NULLIF($5::text,'')::uuid,$6,$7,'post_pending',$8)`,
			requestID, request.EnvironmentID, request.Operation, request.RequestKey,
			nilUUIDString(target.sourceControl), target.workspacePath, params, target.forumID)
		if err == nil {
			err = enqueueDesktopThreadPost(c, tx, requestID, target)
		}
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
		repo.owner || '/' || repo.name, fw.relative_path
		FROM discord_development_environments e
		JOIN discord_forums f ON f.development_environment_id = e.id
		JOIN discord_resources r ON r.id = f.resource_id
		JOIN repositories repo ON repo.id = f.repository_id
		JOIN discord_forum_workspaces fw ON fw.forum_id = f.id
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
			&relative); err != nil {
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
		var sourceForum, sourceEnvironment, sourceConversation, sourceControl uuid.UUID
		err := s.db.QueryRowContext(c.Request.Context(), `SELECT id, discord_conversation_id,
			development_environment_id, (SELECT forum_id FROM discord_conversations
			WHERE id = codex_thread_controls.discord_conversation_id)
			FROM codex_thread_controls WHERE external_thread_id = $1`, sourceThread).
			Scan(&sourceControl, &sourceConversation, &sourceEnvironment, &sourceForum)
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

func enqueueDesktopThreadPost(c *gin.Context, tx *sql.Tx, requestID uuid.UUID,
	target desktopThreadTarget,
) error {
	name := "Codex Desktop · " + target.repository
	if len([]rune(name)) > 100 {
		name = string([]rune(name)[:100])
	}
	card := discordintegration.ComponentCardPayload{AccentColor: 0x5865F2,
		Header: "## 🖥️ Codex Desktop · 正在创建",
		Body:   "Desktop 已连接，正在初始化共享 Codex Thread。",
		Footer: "此 Post 将同步 Desktop 与 Discord 的完整进度和最终回复"}
	payload, _ := json.Marshal(map[string]any{"channelId": target.forumDiscord,
		"threadName": name, "card": card, "desktopThreadRequestId": requestID.String()})
	_, err := tx.ExecContext(c.Request.Context(), `INSERT INTO integration_outbox
		(integration, operation_key, operation_type, route_key, payload, nonce)
		VALUES ('discord',$1,'forum.post.create',$2,$3,$4)
		ON CONFLICT(integration, operation_key) DO NOTHING`,
		"desktop-thread-post:"+requestID.String(), "channels/"+target.forumDiscord+"/threads",
		payload, "desktop-thread-"+requestID.String())
	return err
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
	var environmentID, controlID uuid.UUID
	err = tx.QueryRowContext(c.Request.Context(), `SELECT r.environment_id, r.control_id, r.status
		FROM desktop_thread_requests r JOIN discord_development_environments e
		ON e.id = r.environment_id WHERE r.id = $1 AND e.execution_node_id = $2 FOR UPDATE`,
		requestID, workerNode(c).ID).Scan(&environmentID, &controlID, &status)
	if err != nil {
		problem(c, http.StatusNotFound, "Desktop Thread reservation 不存在", err)
		return
	}
	if environmentID != request.EnvironmentID {
		problem(c, http.StatusForbidden, "Desktop Thread 不属于当前开发环境", nil)
		return
	}
	if status != "completed" {
		if status != "codex_pending" || controlID == uuid.Nil {
			problem(c, http.StatusConflict, "Desktop Thread 尚未准备好或已经失败", nil)
			return
		}
		result, updateErr := tx.ExecContext(c.Request.Context(), `UPDATE codex_thread_controls SET
			external_thread_id = $2, codex_home_key = $3, provider_signature = $4,
			updated_at = now() WHERE id = $1 AND external_thread_id IS NULL`, controlID,
			threadID, environmentID.String(), provider.ConfigSignature)
		if updateErr != nil {
			problem(c, http.StatusConflict, "Codex Thread 已绑定到其他会话", updateErr)
			return
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			problem(c, http.StatusConflict, "Codex Thread Control 已经被修改", nil)
			return
		}
		_, err = tx.ExecContext(c.Request.Context(), `UPDATE desktop_thread_requests SET
			status = 'completed', external_thread_id = $2, response = $3, error = NULL,
			updated_at = now() WHERE id = $1`, requestID, threadID, request.Response)
		if err != nil {
			problem(c, http.StatusInternalServerError, "保存 Desktop Thread 结果失败", err)
			return
		}
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
