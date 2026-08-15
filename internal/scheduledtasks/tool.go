package scheduledtasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
)

func (s *Service) Call(ctx context.Context, tool ToolContext,
	raw json.RawMessage,
) (codex.ToolCallResult, error) {
	if tool.ThreadID == "" || tool.TurnID == "" || tool.CallID == "" {
		return codex.ToolCallResult{}, errors.New("automation tool call 缺少 thread、turn 或 call ID")
	}
	if tool.ExternalThread == "" || tool.ThreadID != tool.ExternalThread {
		return codex.ToolCallResult{}, errors.New("automation tool call thread 与当前 Session 不一致")
	}
	var args ToolArguments
	if err := json.Unmarshal(raw, &args); err != nil {
		return codex.ToolCallResult{}, errors.New("automation_update 参数不是有效 JSON")
	}
	args.Action = strings.TrimSpace(args.Action)
	if args.Action == "" {
		return codex.ToolCallResult{}, errors.New("automation_update 缺少 action")
	}
	callID, duplicate, err := s.beginToolCall(ctx, tool, raw)
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	if duplicate {
		return s.previousToolResult(ctx, tool, raw)
	}

	var payload any
	switch args.Action {
	case "create":
		payload, err = s.Create(ctx, tool, args)
	case "update":
		payload, err = s.Update(ctx, tool, args)
	case "list":
		var tasks []Task
		tasks, err = s.List(ctx, tool, args.IncludeDeleted)
		payload = map[string]any{"tasks": tasks}
	case "delete":
		if args.TaskID == nil {
			err = errors.New("delete 缺少 task_id")
		} else {
			payload, err = s.Delete(ctx, tool, *args.TaskID)
		}
	case "run_now":
		if args.TaskID == nil {
			err = errors.New("run_now 缺少 task_id")
		} else {
			var run Run
			var deduplicated bool
			run, deduplicated, err = s.RunNow(ctx, tool, *args.TaskID)
			payload = map[string]any{"run": run, "deduplicated": deduplicated}
		}
	default:
		err = fmt.Errorf("未知 automation_update action %q", args.Action)
	}
	if err != nil {
		s.failToolCall(ctx, callID, err)
		return codex.ToolCallResult{}, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		s.failToolCall(ctx, callID, err)
		return codex.ToolCallResult{}, err
	}
	result := codex.TextToolResult(string(encoded), true)
	resultJSON, _ := json.Marshal(result)
	if _, err = s.db.ExecContext(ctx, `UPDATE tool_calls SET status='completed',result=$2,
		finished_at=now() WHERE id=$1`, callID, resultJSON); err != nil {
		return codex.ToolCallResult{}, err
	}
	return result, nil
}

func (s *Service) beginToolCall(ctx context.Context, tool ToolContext,
	raw json.RawMessage,
) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := s.db.QueryRowContext(ctx, `INSERT INTO tool_calls(
		run_id,intent_id,thread_id,turn_id,call_id,namespace,tool,arguments)
		VALUES ($1,$2,$3,$4,$5,'tyrs_hand','automation_update',$6)
		ON CONFLICT(thread_id,turn_id,call_id) DO NOTHING RETURNING id`, tool.RunID,
		tool.IntentID, tool.ThreadID, tool.TurnID, tool.CallID, raw).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, true, nil
	}
	return id, false, err
}

func (s *Service) previousToolResult(ctx context.Context, tool ToolContext,
	raw json.RawMessage,
) (codex.ToolCallResult, error) {
	var status string
	var resultJSON []byte
	var message sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT status,result,error FROM tool_calls
		WHERE thread_id=$1 AND turn_id=$2 AND call_id=$3
		  AND namespace='tyrs_hand' AND tool='automation_update' AND arguments=$4::jsonb`,
		tool.ThreadID, tool.TurnID, tool.CallID, string(raw)).Scan(&status, &resultJSON, &message)
	if errors.Is(err, sql.ErrNoRows) {
		return codex.ToolCallResult{}, errors.New("tool call ID 与既有请求不一致")
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
	return codex.ToolCallResult{}, errors.New("同一 automation tool call 正在执行")
}

func (s *Service) failToolCall(ctx context.Context, id uuid.UUID, cause error) {
	_, _ = s.db.ExecContext(ctx, `UPDATE tool_calls SET status='failed',error=$2,
		finished_at=now() WHERE id=$1`, id, cause.Error())
}
