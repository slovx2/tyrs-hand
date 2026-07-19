package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	ghadapter "github.com/slovx2/tyrs-hand/internal/github"
	"github.com/slovx2/tyrs-hand/internal/githubtools"
	"github.com/slovx2/tyrs-hand/internal/security"
)

type CallRequest struct {
	Capability string          `json:"capability"`
	ThreadID   string          `json:"threadId"`
	TurnID     string          `json:"turnId"`
	CallID     string          `json:"callId"`
	Namespace  string          `json:"namespace"`
	Tool       string          `json:"tool"`
	Arguments  json.RawMessage `json:"arguments"`
}

type Service struct {
	db      *sql.DB
	app     *ghadapter.AppClient
	catalog *githubtools.Catalog
}

type authorization struct {
	AttemptID             uuid.UUID
	SourceType            string
	WorkItemID            uuid.UUID
	ConversationID        uuid.UUID
	InstallationID        int64
	Owner                 string
	Repository            string
	Number                int
	AllowedNumbers        []int
	Actor                 string
	Kind                  string
	AgentOwned            bool
	AllowedTools          []string
	Contributors          []string
	HasUnboundContributor bool
}

func NewService(db *sql.DB, app *ghadapter.AppClient, catalog *githubtools.Catalog) *Service {
	return &Service{db: db, app: app, catalog: catalog}
}

func (s *Service) GitCredential(ctx context.Context, capability, purpose, turnID string) (string, error) {
	auth, err := s.authorize(ctx, capability, turnID)
	if err != nil {
		return "", err
	}
	if purpose != "fetch" && purpose != "push" {
		return "", errors.New("请求的 Git 凭据用途无效")
	}
	if auth.SourceType == "discord_conversation" {
		if err := s.requireDiscordPermission(ctx, auth, purpose == "push"); err != nil {
			return "", err
		}
	} else if purpose == "push" && !auth.AgentOwned {
		if err := s.requireWritePermission(ctx, auth); err != nil {
			return "", err
		}
	}
	return s.app.InstallationToken(ctx, auth.InstallationID)
}

func (s *Service) Call(ctx context.Context, request CallRequest) (codex.ToolCallResult, error) {
	if request.Namespace != "github" {
		return codex.ToolCallResult{}, errors.New("控制面只执行 github namespace 工具")
	}
	auth, err := s.authorize(ctx, request.Capability, request.TurnID)
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	if !contains(auth.AllowedTools, request.Tool) {
		return codex.ToolCallResult{}, fmt.Errorf("工具 %s 不在当前任务允许列表中", request.Tool)
	}
	readOnly, ok := s.catalog.IsReadOnly(request.Tool)
	if !ok {
		return codex.ToolCallResult{}, fmt.Errorf("工具 %s 未注册", request.Tool)
	}
	if auth.SourceType == "discord_conversation" {
		if err := s.requireDiscordPermission(ctx, auth, !readOnly); err != nil {
			return codex.ToolCallResult{}, err
		}
	} else if !readOnly && !auth.AgentOwned {
		if err := s.requireWritePermission(ctx, auth); err != nil {
			return codex.ToolCallResult{}, err
		}
	}
	if err := validateArguments(request.Arguments, auth); err != nil {
		return codex.ToolCallResult{}, err
	}

	var callID uuid.UUID
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO tool_calls(job_attempt_id, thread_id, turn_id, call_id, namespace, tool, arguments)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT(thread_id, turn_id, call_id) DO NOTHING
		RETURNING id`, auth.AttemptID, request.ThreadID, request.TurnID, request.CallID,
		request.Namespace, request.Tool, request.Arguments).Scan(&callID)
	if errors.Is(err, sql.ErrNoRows) {
		result, previousErr := s.previousResult(ctx, request)
		if previousErr == nil && request.Tool == "create_pull_request" && auth.SourceType == "github_work_item" {
			previousErr = s.linkCreatedPullRequest(ctx, auth, result)
		}
		return result, previousErr
	}
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	deps, err := s.app.ToolDependencies(ctx, auth.InstallationID)
	if err != nil {
		s.fail(ctx, callID, err)
		return codex.ToolCallResult{}, err
	}
	result, err := s.catalog.Execute(ctx, deps, request.Tool, request.Arguments)
	if err != nil {
		s.fail(ctx, callID, err)
		return codex.ToolCallResult{}, err
	}
	resultJSON, _ := json.Marshal(result)
	_, err = s.db.ExecContext(ctx, `UPDATE tool_calls SET status = 'completed', result = $2, finished_at = now() WHERE id = $1`, callID, resultJSON)
	if err == nil && request.Tool == "create_pull_request" && auth.SourceType == "github_work_item" {
		err = s.linkCreatedPullRequest(ctx, auth, result)
	}
	return result, err
}

func (s *Service) authorize(ctx context.Context, capability, turnID string) (authorization, error) {
	if capability == "" {
		return authorization{}, errors.New("任务 Capability 不能为空")
	}
	var auth authorization
	var toolsJSON, dangerousJSON []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT a.id, j.source_type, j.work_item_id, j.discord_conversation_id,
			i.external_id, r.owner, r.name, COALESCE(w.external_number, 0),
			j.actor_login, COALESCE(w.kind, ''), COALESCE(w.agent_owned, false), j.allowed_tools, j.dangerous_actions
		FROM job_attempts a
		JOIN job_intents j ON j.id = a.job_id
		JOIN repositories r ON r.id = j.repository_id
		JOIN scm_installations i ON i.id = r.installation_id
		LEFT JOIN work_items w ON w.id = j.work_item_id
		WHERE a.capability_hash = $1 AND a.status = 'running'
		  AND j.status = 'running' AND j.lease_epoch = a.lease_epoch
		  AND j.lease_expires_at > now()`, security.Digest(capability)).
		Scan(&auth.AttemptID, &auth.SourceType, &auth.WorkItemID, &auth.ConversationID,
			&auth.InstallationID, &auth.Owner, &auth.Repository, &auth.Number,
			&auth.Actor, &auth.Kind, &auth.AgentOwned, &toolsJSON, &dangerousJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return authorization{}, errors.New("任务 Capability 已失效")
	}
	if err != nil {
		return authorization{}, err
	}
	if err := json.Unmarshal(toolsJSON, &auth.AllowedTools); err != nil {
		return authorization{}, err
	}
	var dangerous []string
	if err := json.Unmarshal(dangerousJSON, &dangerous); err != nil {
		return authorization{}, err
	}
	auth.AllowedTools = append(auth.AllowedTools, dangerous...)
	if auth.SourceType == "discord_conversation" {
		if err := s.loadDiscordContributors(ctx, capability, turnID, &auth); err != nil {
			return authorization{}, err
		}
		return auth, nil
	}
	auth.AllowedNumbers = []int{auth.Number}
	rows, err := s.db.QueryContext(ctx, "SELECT external_number FROM work_item_channels WHERE work_item_id = $1", auth.WorkItemID)
	if err != nil {
		return authorization{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var number int
		if err := rows.Scan(&number); err != nil {
			return authorization{}, err
		}
		auth.AllowedNumbers = append(auth.AllowedNumbers, number)
	}
	if err := rows.Err(); err != nil {
		return authorization{}, err
	}
	return auth, nil
}

func (s *Service) linkCreatedPullRequest(ctx context.Context, auth authorization, result codex.ToolCallResult) error {
	number := pullRequestNumber(result)
	if number <= 0 {
		return errors.New("远端已创建 GitHub PR，但结果中缺少 PR Number，无法关联 Work Item")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO work_item_channels(work_item_id, channel_type, external_number, metadata)
		VALUES ($1, 'pull_request', $2, '{"created_by":"agent"}'::jsonb)
		ON CONFLICT(work_item_id, channel_type, external_number) DO NOTHING`, auth.WorkItemID, number)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE work_items SET agent_owned = true, updated_at = now() WHERE id = $1`, auth.WorkItemID)
	return err
}

var pullURLPattern = regexp.MustCompile(`/pull/([0-9]+)`)

func pullRequestNumber(result codex.ToolCallResult) int {
	for _, item := range result.ContentItems {
		if item.Text == "" {
			continue
		}
		var value any
		if json.Unmarshal([]byte(item.Text), &value) == nil {
			if number := findNumber(value); number > 0 {
				return number
			}
		}
		if match := pullURLPattern.FindStringSubmatch(item.Text); len(match) == 2 {
			number, _ := strconv.Atoi(match[1])
			return number
		}
	}
	return 0
}

func findNumber(value any) int {
	switch typed := value.(type) {
	case map[string]any:
		if number, ok := typed["number"].(float64); ok {
			return int(number)
		}
		for _, nested := range typed {
			if number := findNumber(nested); number > 0 {
				return number
			}
		}
	case []any:
		for _, nested := range typed {
			if number := findNumber(nested); number > 0 {
				return number
			}
		}
	}
	return 0
}

func (s *Service) requireWritePermission(ctx context.Context, auth authorization) error {
	permission, err := s.app.Permission(ctx, auth.InstallationID, auth.Owner, auth.Repository, auth.Actor)
	if err != nil {
		return fmt.Errorf("发布前读取触发者权限: %w", err)
	}
	if permission != "write" && permission != "maintain" && permission != "admin" && permission != "push" {
		return errors.New("当前触发者不再具备 write 以上权限")
	}
	return nil
}

func (s *Service) loadDiscordContributors(ctx context.Context, capability, turnID string, auth *authorization) error {
	query := `SELECT COALESCE(b.github_login, ''), b.id IS NULL
		FROM discord_turn_contributors c
		LEFT JOIN discord_identity_bindings b ON b.id = c.github_binding_id
			AND b.status = 'active' AND b.github_user_id = c.github_user_id
		WHERE c.conversation_id = $1 AND c.external_turn_id = $2
		ORDER BY c.contributed_at, c.discord_user_id`
	rows, err := s.db.QueryContext(ctx, query, auth.ConversationID, turnID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var login string
		var unbound bool
		if err := rows.Scan(&login, &unbound); err != nil {
			return err
		}
		auth.HasUnboundContributor = auth.HasUnboundContributor || unbound || login == ""
		if login != "" {
			auth.Contributors = append(auth.Contributors, login)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(auth.Contributors) > 0 || auth.HasUnboundContributor || turnID != "" {
		return nil
	}
	// 初次拉取 Worktree 发生在 Turn 建立前，只能使用本次消息的绑定快照。
	var login string
	var unbound bool
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(b.github_login, ''), b.id IS NULL
		FROM job_attempts a JOIN job_intents j ON j.id = a.job_id
		JOIN discord_input_messages m ON m.message_id = j.discord_message_id
		LEFT JOIN discord_identity_bindings b ON b.id = m.github_binding_id
			AND b.status = 'active' AND b.github_user_id = m.github_user_id
		WHERE a.capability_hash = $1`, security.Digest(capability)).Scan(&login, &unbound)
	if err != nil {
		return err
	}
	auth.HasUnboundContributor = unbound || login == ""
	if login != "" {
		auth.Contributors = append(auth.Contributors, login)
	}
	return nil
}

func (s *Service) requireDiscordPermission(ctx context.Context, auth authorization, write bool) error {
	if auth.HasUnboundContributor || len(auth.Contributors) == 0 {
		return errors.New("当前 Turn 包含未绑定或已经解绑的 Discord 贡献者，GitHub 操作被拒绝")
	}
	required := 1
	if write {
		required = 3
	}
	for _, login := range auth.Contributors {
		permission, err := s.app.Permission(ctx, auth.InstallationID, auth.Owner, auth.Repository, login)
		if err != nil {
			return fmt.Errorf("实时读取贡献者 %s 的 GitHub 权限: %w", login, err)
		}
		if githubPermissionRank(permission) < required {
			return fmt.Errorf("贡献者 %s 的 GitHub 权限 %s 低于当前操作要求", login, permission)
		}
	}
	return nil
}

func githubPermissionRank(permission string) int {
	switch permission {
	case "admin":
		return 5
	case "maintain":
		return 4
	case "write", "push":
		return 3
	case "triage":
		return 2
	case "read", "pull":
		return 1
	default:
		return 0
	}
}

func (s *Service) previousResult(ctx context.Context, request CallRequest) (codex.ToolCallResult, error) {
	var status string
	var resultJSON []byte
	var message sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT status, result, error FROM tool_calls
		WHERE thread_id = $1 AND turn_id = $2 AND call_id = $3
		  AND namespace = $4 AND tool = $5 AND arguments = $6::jsonb`,
		request.ThreadID, request.TurnID, request.CallID, request.Namespace, request.Tool,
		string(request.Arguments)).Scan(&status, &resultJSON, &message)
	if errors.Is(err, sql.ErrNoRows) {
		return codex.ToolCallResult{}, errors.New("Tool Call ID 与既有请求不一致")
	}
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	if status == "completed" {
		var result codex.ToolCallResult
		if err := json.Unmarshal(resultJSON, &result); err != nil {
			return codex.ToolCallResult{}, err
		}
		return result, nil
	}
	if status == "failed" {
		return codex.ToolCallResult{}, errors.New(message.String)
	}
	return codex.ToolCallResult{}, errors.New("同一 Tool Call 正在执行，不能重复提交")
}

func (s *Service) fail(ctx context.Context, id uuid.UUID, cause error) {
	_, _ = s.db.ExecContext(ctx, `UPDATE tool_calls SET status = 'failed', error = $2, finished_at = now() WHERE id = $1`, id, cause.Error())
}

func validateArguments(raw json.RawMessage, auth authorization) error {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return errors.New("工具参数不是有效 JSON 对象")
	}
	if owner, ok := args["owner"].(string); ok && !strings.EqualFold(owner, auth.Owner) {
		return errors.New("工具参数越过了当前仓库 owner 边界")
	}
	if repo, ok := args["repo"].(string); ok && !strings.EqualFold(repo, auth.Repository) {
		return errors.New("工具参数越过了当前仓库边界")
	}
	for _, key := range []string{"issueNumber", "pullNumber", "issue_number", "pull_number", "pull_request_number"} {
		if value, ok := args[key]; ok {
			number, valid := argumentNumber(value)
			if !valid || !containsNumber(auth.AllowedNumbers, number) {
				return errors.New("工具参数越过了当前 Issue/PR 边界")
			}
		}
	}
	return nil
}

func argumentNumber(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), typed == float64(int(typed))
	case string:
		number, err := strconv.Atoi(typed)
		return number, err == nil
	default:
		return 0, false
	}
}

func containsNumber(values []int, expected int) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
