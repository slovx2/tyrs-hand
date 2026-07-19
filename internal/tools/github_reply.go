package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
)

const maxGitHubReplyBytes = 60000

var (
	absoluteWorkerPath = regexp.MustCompile(`(?m)(?:/data/worker|/opt/tyrs-hand|/Volumes/workspace)/[^\s]+`)
	credentialText     = regexp.MustCompile(`(?i)(token|password|api[_-]?key|secret)\s*[:=]\s*[^\s]+`)
)

func (s *Service) replyToGitHub(ctx context.Context, auth authorization,
	request CallRequest,
) (codex.ToolCallResult, error) {
	if auth.SourceType != "github_work_item" || auth.WorkItemID == uuid.Nil {
		return codex.ToolCallResult{}, errors.New("最终 GitHub 回复只允许当前 GitHub Work Item 使用")
	}
	var arguments struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(request.Arguments, &arguments); err != nil {
		return codex.ToolCallResult{}, errors.New("最终回复参数不是有效 JSON")
	}
	body := sanitizeGitHubReply(arguments.Body)
	if body == "" {
		return codex.ToolCallResult{}, errors.New("最终回复正文不能为空")
	}
	marker := fmt.Sprintf("<!-- tyrs-hand:intent:%s -->", auth.IntentID)
	if len(body)+len(marker)+2 > maxGitHubReplyBytes {
		body = body[:maxGitHubReplyBytes-len(marker)-2]
		body = strings.TrimSpace(body)
	}
	body += "\n\n" + marker

	callID, existing, err := s.persistReplyCall(ctx, auth, request)
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	if existing {
		if result, previousErr := s.previousResult(ctx, request); previousErr == nil {
			return result, nil
		} else if !strings.Contains(previousErr.Error(), "正在执行") {
			return codex.ToolCallResult{}, previousErr
		}
	}
	release, err := s.lockReplyIntent(ctx, auth.IntentID)
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	defer release()
	if result, delivered, err := s.deliveredReply(ctx, auth.IntentID); err != nil {
		return codex.ToolCallResult{}, err
	} else if delivered {
		s.completeReplyCall(ctx, callID, result)
		return result, nil
	}
	_, err = s.db.ExecContext(ctx, `UPDATE codex_turn_intents SET reply_status = 'sending',
		reply_tool_call_id = COALESCE(reply_tool_call_id, $2), updated_at = now()
		WHERE id = $1 AND reply_status IN ('pending','sending')`, auth.IntentID, request.CallID)
	if err != nil {
		return codex.ToolCallResult{}, err
	}

	comment, found, err := s.app.FindIssueComment(ctx, auth.InstallationID, auth.Owner,
		auth.Repository, auth.Number, marker)
	if err == nil && !found {
		comment, err = s.app.CreateIssueComment(ctx, auth.InstallationID, auth.Owner,
			auth.Repository, auth.Number, body)
	}
	if err != nil {
		// 创建响应不确定时，按 Intent Marker 对账，禁止盲目重复写入。
		comment, found, _ = s.app.FindIssueComment(ctx, auth.InstallationID, auth.Owner,
			auth.Repository, auth.Number, marker)
		if !found {
			s.fail(ctx, callID, err)
			_, _ = s.db.ExecContext(ctx, `UPDATE codex_turn_intents SET reply_status = 'failed',
				last_error_code = 'github_reply_failed', last_error_message = $2, updated_at = now()
				WHERE id = $1`, auth.IntentID, err.Error())
			return codex.ToolCallResult{}, err
		}
	}
	result := codex.TextToolResult(fmt.Sprintf(`{"commentId":%d,"url":%q}`, comment.ID, comment.URL), true)
	resultJSON, _ := json.Marshal(result)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `UPDATE codex_turn_intents SET reply_status = 'delivered',
		github_comment_id = $2, github_comment_url = $3, updated_at = now() WHERE id = $1`,
		auth.IntentID, comment.ID, comment.URL); err != nil {
		return codex.ToolCallResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tool_calls SET status = 'completed', result = $2,
		finished_at = now() WHERE id = $1`, callID, resultJSON); err != nil {
		return codex.ToolCallResult{}, err
	}
	return result, tx.Commit()
}

func (s *Service) lockReplyIntent(ctx context.Context, intentID uuid.UUID) (func(), error) {
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = connection.ExecContext(ctx,
		`SELECT pg_advisory_lock(hashtextextended($1, 0))`, intentID.String()); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = connection.ExecContext(unlockCtx,
			`SELECT pg_advisory_unlock(hashtextextended($1, 0))`, intentID.String())
		_ = connection.Close()
	}, nil
}

func (s *Service) persistReplyCall(ctx context.Context, auth authorization,
	request CallRequest,
) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := s.db.QueryRowContext(ctx, `INSERT INTO tool_calls
		(run_id, intent_id, thread_id, turn_id, call_id, namespace, tool, arguments)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(thread_id, turn_id, call_id) DO NOTHING
		RETURNING id`, auth.RunID, auth.IntentID, request.ThreadID, request.TurnID,
		request.CallID, request.Namespace, request.Tool, request.Arguments).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = s.db.QueryRowContext(ctx, `SELECT id FROM tool_calls
			WHERE thread_id=$1 AND turn_id=$2 AND call_id=$3`, request.ThreadID,
			request.TurnID, request.CallID).Scan(&id)
		return id, true, err
	}
	return id, false, err
}

func (s *Service) deliveredReply(ctx context.Context, intentID uuid.UUID) (codex.ToolCallResult, bool, error) {
	var status string
	var commentID sql.NullInt64
	var url sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT reply_status, github_comment_id, github_comment_url
		FROM codex_turn_intents WHERE id = $1`, intentID).Scan(&status, &commentID, &url)
	if err != nil || status != "delivered" {
		return codex.ToolCallResult{}, false, err
	}
	return codex.TextToolResult(fmt.Sprintf(`{"commentId":%d,"url":%q}`, commentID.Int64, url.String), true), true, nil
}

func (s *Service) completeReplyCall(ctx context.Context, callID uuid.UUID, result codex.ToolCallResult) {
	data, _ := json.Marshal(result)
	_, _ = s.db.ExecContext(ctx, `UPDATE tool_calls SET status = 'completed', result = $2,
		finished_at = now() WHERE id = $1`, callID, data)
}

func sanitizeGitHubReply(value string) string {
	value = strings.TrimSpace(value)
	value = absoluteWorkerPath.ReplaceAllString(value, "[managed path]")
	value = credentialText.ReplaceAllString(value, "$1=[redacted]")
	return value
}

func (s *Service) ReportFailure(ctx context.Context, capability, code string) error {
	auth, err := s.authorize(ctx, capability, "")
	if err != nil {
		return err
	}
	if auth.SourceType != "github_work_item" {
		return nil
	}
	body := "Tyrs Hand 在控制 Codex 执行时遇到平台错误，本轮任务未完成。请稍后重试；若问题持续出现，请联系管理员并提供错误代码 `" +
		sanitizeFailureCode(code) + "`。"
	arguments, _ := json.Marshal(map[string]string{"body": body})
	_, err = s.replyToGitHub(ctx, auth, CallRequest{
		Capability: capability, ThreadID: "system-" + auth.IntentID.String(),
		TurnID: "system-" + auth.IntentID.String(), CallID: "failure-" + auth.IntentID.String(),
		Namespace: "tyrs_hand", Tool: "reply_to_github", Arguments: arguments,
	})
	return err
}

func sanitizeFailureCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "control_error"
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') &&
			character != '_' && character != '-' {
			return "control_error"
		}
	}
	return value
}
