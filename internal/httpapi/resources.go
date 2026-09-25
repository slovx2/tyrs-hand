package httpapi

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"

	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/githubtools"
)

type agentProfileRequest struct {
	Name            string         `json:"name" binding:"required"`
	Model           string         `json:"model"`
	ReasoningEffort string         `json:"reasoningEffort"`
	ServiceTier     string         `json:"serviceTier"`
	Sandbox         string         `json:"sandbox"`
	NetworkEnabled  *bool          `json:"networkEnabled"`
	AllowedTools    []string       `json:"allowedTools"`
	Config          map[string]any `json:"config"`
}

func (s *Server) createAgentProfile(c *gin.Context) {
	var request agentProfileRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	if request.Sandbox == "" {
		request.Sandbox = "workspace-write"
	}
	if len(request.AllowedTools) == 0 {
		request.AllowedTools = githubtools.DefaultAllowedTools
	}
	network := true
	if request.NetworkEnabled != nil {
		network = *request.NetworkEnabled
	}
	var id uuid.UUID
	err := s.db.QueryRowContext(c, `
		INSERT INTO agent_profiles(name, model, reasoning_effort, service_tier, sandbox, network_enabled, allowed_tools, config)
		VALUES ($1, NULLIF($2,''), NULLIF($3,''), NULLIF($4,''), $5, $6, $7, $8)
		RETURNING id`, request.Name, request.Model, request.ReasoningEffort, request.ServiceTier,
		request.Sandbox, network, encodeJSON(request.AllowedTools), encodeJSON(request.Config)).Scan(&id)
	if err != nil {
		problem(c, http.StatusConflict, "创建 Agent Profile 失败", err)
		return
	}
	s.audit(c, "agent_profile.create", "agent_profile", id.String(), map[string]any{"name": request.Name})
	c.JSON(http.StatusCreated, gin.H{"id": id})
}

func (s *Server) listAgentProfiles(c *gin.Context) {
	s.listRows(c, `SELECT id, name, model, reasoning_effort, service_tier, sandbox, network_enabled, allowed_tools, updated_at FROM agent_profiles ORDER BY name`,
		[]string{"id", "name", "model", "reasoningEffort", "serviceTier", "sandbox", "networkEnabled", "allowedTools", "updatedAt"})
}

type triggerRuleRequest struct {
	RepositoryID       uuid.UUID      `json:"repositoryId" binding:"required"`
	AgentProfileID     uuid.UUID      `json:"agentProfileId" binding:"required"`
	Name               string         `json:"name" binding:"required"`
	EventName          string         `json:"eventName" binding:"required"`
	Action             string         `json:"action"`
	Enabled            *bool          `json:"enabled"`
	Priority           int            `json:"priority"`
	TriggerKind        string         `json:"triggerKind" binding:"required"`
	TriggerValue       string         `json:"triggerValue"`
	ActorMinPermission string         `json:"actorMinPermission"`
	Instruction        string         `json:"instruction" binding:"required"`
	Skills             []string       `json:"skills"`
	AllowedTools       []string       `json:"allowedTools"`
	DangerousActions   []string       `json:"dangerousActions"`
	Filters            map[string]any `json:"filters"`
}

var slashCommandName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func triggerRuleEnabled(request triggerRuleRequest) bool {
	if request.Enabled != nil {
		return *request.Enabled
	}
	return request.TriggerKind != "legacy_mention"
}

func validateTriggerRule(request *triggerRuleRequest) error {
	switch request.TriggerKind {
	case "event":
		if request.TriggerValue != "" {
			return fmt.Errorf("event 触发规则不能设置 triggerValue")
		}
	case "label":
		if request.EventName != "issues" && request.EventName != "pull_request" {
			return fmt.Errorf("label 触发规则只支持 issues 或 pull_request 事件")
		}
		if request.TriggerValue == "" {
			return fmt.Errorf("label 触发规则必须设置 triggerValue")
		}
		if request.Action == "" {
			request.Action = "labeled"
		}
		if request.Action != "labeled" {
			return fmt.Errorf("label 触发规则的 action 必须是 labeled")
		}
	case "slash_command":
		if request.EventName != "issue_comment" && request.EventName != "pull_request_review_comment" {
			return fmt.Errorf("slash_command 只支持评论事件")
		}
		if !slashCommandName.MatchString(request.TriggerValue) {
			return fmt.Errorf("slash_command 的 triggerValue 必须是小写命令名，且不能包含斜杠")
		}
		if request.Action == "" {
			request.Action = "created"
		}
		if request.Action != "created" && request.Action != "edited" {
			return fmt.Errorf("slash_command 的 action 只支持 created 或 edited")
		}
	case "mention_command":
		if request.EventName != "issue_comment" {
			return fmt.Errorf("mention_command 只支持 issue_comment 事件")
		}
		if request.TriggerValue != "" {
			return fmt.Errorf("mention_command 使用 GitHub App 登录名，不能设置 triggerValue")
		}
		if request.Action == "" {
			request.Action = "created"
		}
		if request.Action != "created" {
			return fmt.Errorf("mention_command 的 action 只支持 created")
		}
	case "legacy_mention":
		if request.EventName != "issue_comment" && request.EventName != "pull_request_review_comment" {
			return fmt.Errorf("legacy_mention 只支持评论事件")
		}
		if request.TriggerValue != "" {
			return fmt.Errorf("legacy_mention 使用 GitHub App 登录名，不能设置 triggerValue")
		}
		if request.Action == "" {
			request.Action = "created"
		}
		if request.Action != "created" && request.Action != "edited" {
			return fmt.Errorf("legacy_mention 的 action 只支持 created 或 edited")
		}
	default:
		return fmt.Errorf("不支持的 triggerKind %q", request.TriggerKind)
	}
	return nil
}

func (s *Server) listWorkItems(c *gin.Context) {
	s.listRows(c, `SELECT w.id, w.kind, w.external_number, w.title, w.state, w.agent_owned,
		w.head_sha, w.updated_at, r.owner, r.name, n.name FROM work_items w
		JOIN repositories r ON r.id = w.repository_id
		LEFT JOIN workers n ON n.id = w.worker_id
		ORDER BY w.updated_at DESC LIMIT 200`, []string{"id", "kind", "number", "title",
		"state", "agentOwned", "headSha", "updatedAt", "owner", "repository", "worker"})
}

func (s *Server) listJobs(c *gin.Context) {
	s.listRows(c, `SELECT i.id, i.work_item_id, i.trigger_rule_id, i.trigger_evidence, i.status,
		i.priority, i.attempt_count, i.max_attempts, c.lease_owner, c.lease_epoch,
		c.lease_expires_at, i.last_error_message, i.created_at, i.updated_at, n.name
		FROM codex_turn_intents i JOIN codex_thread_controls c ON c.id = i.control_id
		LEFT JOIN workers n ON n.id = c.worker_id
		ORDER BY i.created_at DESC LIMIT 200`,
		[]string{"id", "workItemId", "triggerRuleId", "triggerEvidence", "status", "priority", "attemptCount", "maxAttempts", "workerId", "leaseEpoch", "leaseExpiresAt", "lastError", "createdAt", "updatedAt", "worker"})
}

func (s *Server) listThreads(c *gin.Context) {
	s.listRows(c, `SELECT t.id, t.source_type, t.external_thread_id, t.status,
		t.active_codex_turn_id, t.updated_at, t.lease_expires_at, w.kind, w.external_number, n.name
		FROM codex_thread_controls t LEFT JOIN work_items w ON w.id = t.work_item_id
		LEFT JOIN workers n ON n.id = t.worker_id
		ORDER BY t.updated_at DESC LIMIT 200`,
		[]string{"id", "sourceType", "threadId", "status", "lastTurnId", "lastUsedAt", "expiresAt", "kind", "number", "worker"})
}

func (s *Server) listWorktrees(c *gin.Context) {
	s.listRows(c, `SELECT wt.id, 'github_work_item' AS kind, wt.path, wt.branch,
		wt.base_sha, wt.head_sha, wt.status, wt.dirty, wt.last_used_at, wt.expires_at,
		rc.path AS cache_path, rc.size_bytes, rc.last_fetch_at, n.name
		FROM worktrees wt JOIN repo_caches rc ON rc.id = wt.repo_cache_id
		LEFT JOIN workers n ON n.id = wt.worker_id
		ORDER BY wt.last_used_at DESC LIMIT 200`,
		[]string{"id", "kind", "path", "branch", "baseSha", "headSha", "status", "dirty",
			"lastUsedAt", "expiresAt", "cachePath", "cacheSizeBytes", "lastFetchAt", "worker"})
}

func (s *Server) listRepoCaches(c *gin.Context) {
	s.listRows(c, `SELECT rc.id, rc.path, rc.status, rc.size_bytes, rc.last_fetch_at,
		rc.last_used_at, rc.error, r.owner, r.name, n.name
		FROM repo_caches rc JOIN repositories r ON r.id = rc.repository_id
		LEFT JOIN workers n ON n.id = rc.worker_id
		ORDER BY rc.last_used_at DESC`,
		[]string{"id", "path", "status", "sizeBytes", "lastFetchAt", "lastUsedAt", "error", "owner", "repository", "worker"})
}

func (s *Server) systemStatus(c *gin.Context) {
	counts := map[string]int64{}
	for name, query := range map[string]string{
		"queuedJobs":    "SELECT count(*) FROM codex_turn_intents WHERE status IN ('queued','retry_wait')",
		"runningJobs":   "SELECT count(*) FROM codex_turn_intents WHERE status IN ('dispatching','awaiting_confirmation','running','reconciling')",
		"onlineWorkers": "SELECT count(*) FROM workers WHERE status = 'online' AND heartbeat_at > now() - interval '2 minutes'",
		"activeThreads": "SELECT count(*) FROM codex_thread_controls WHERE status = 'active'",
	} {
		var count int64
		if err := s.db.QueryRowContext(c, query).Scan(&count); err != nil {
			problem(c, http.StatusInternalServerError, "读取系统状态失败", err)
			return
		}
		counts[name] = count
	}
	c.JSON(http.StatusOK, counts)
}

func (s *Server) listAuditLogs(c *gin.Context) {
	s.listRows(c, `SELECT id, action, resource_type, resource_id, request_id, metadata, created_at FROM audit_logs ORDER BY id DESC LIMIT 200`,
		[]string{"id", "action", "resourceType", "resourceId", "requestId", "metadata", "createdAt"})
}

func (s *Server) listRows(c *gin.Context, query string, names []string) {
	limit := 50
	if value := c.Query("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			badRequest(c, fmt.Errorf("limit 必须在 1 到 200 之间"))
			return
		}
		limit = parsed
	}
	offset := 0
	if cursor := c.Query("cursor"); cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			badRequest(c, fmt.Errorf("cursor 无效"))
			return
		}
		offset, err = strconv.Atoi(string(decoded))
		if err != nil || offset < 0 {
			badRequest(c, fmt.Errorf("cursor 无效"))
			return
		}
	}
	rows, err := s.db.QueryContext(c, query)
	if err != nil {
		problem(c, http.StatusInternalServerError, "读取数据失败", err)
		return
	}
	defer func() { _ = rows.Close() }()
	items := make([]map[string]any, 0, limit+1)
	rowIndex := 0
	for rows.Next() {
		values := make([]any, len(names))
		pointers := make([]any, len(names))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			problem(c, http.StatusInternalServerError, "解析数据失败", err)
			return
		}
		if rowIndex < offset {
			rowIndex++
			continue
		}
		rowIndex++
		if len(items) >= limit+1 {
			continue
		}
		item := make(map[string]any, len(names))
		for index, name := range names {
			item[name] = normalizeSQLValue(values[index])
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		problem(c, http.StatusInternalServerError, "读取数据失败", err)
		return
	}
	response := gin.H{"items": items}
	if len(items) > limit {
		response["items"] = items[:limit]
		response["nextCursor"] = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset + limit)))
	}
	c.JSON(http.StatusOK, response)
}

func normalizeSQLValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		var decoded any
		if json.Unmarshal(typed, &decoded) == nil {
			return decoded
		}
		return string(typed)
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	default:
		return value
	}
}

func encodeJSON(value any) []byte {
	if value == nil {
		value = map[string]any{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("无法编码内部 JSON: %v", err))
	}
	return data
}

var _ = sql.ErrNoRows
