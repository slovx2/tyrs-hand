package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
)

func isLiveVoiceTool(name string) bool {
	switch name {
	case "list_sessions", "create_session", "send_message", "read_session", "transfer_voice_call", "end_voice_call":
		return true
	default:
		return false
	}
}

func (s *Server) callLiveVoiceTool(ctx context.Context, claimed *codexcontrol.ClaimedControl,
	_, tool string, raw json.RawMessage,
) (codex.ToolCallResult, error) {
	if claimed == nil || claimed.SessionID == uuid.Nil {
		return codex.ToolCallResult{}, errors.New("语音工具只允许 Workspace Session 使用")
	}
	var liveID uuid.UUID
	var workerID uuid.UUID
	err := s.db.QueryRowContext(ctx, `SELECT id, worker_id FROM live_conversations
		WHERE workspace_session_id=$1 AND active_session_id IS NOT NULL`, claimed.SessionID).
		Scan(&liveID, &workerID)
	if errors.Is(err, sql.ErrNoRows) {
		return codex.TextToolResult("当前没有活动的语音绑定", false), nil
	}
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	args := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return codex.ToolCallResult{}, errors.New("工具参数无效")
		}
	}
	switch tool {
	case "list_sessions":
		return s.liveVoiceListSessions(ctx, workerID)
	case "create_session":
		return s.liveVoiceCreateSession(ctx, claimed, liveID, workerID, args)
	case "send_message":
		return s.liveVoiceSendMessage(ctx, liveID, workerID, args)
	case "read_session":
		return s.liveVoiceReadSession(ctx, workerID, args)
	case "transfer_voice_call":
		return s.liveVoiceTransfer(ctx, liveID, claimed.SessionID, workerID, args)
	case "end_voice_call":
		return s.liveVoiceEnd(ctx, liveID)
	default:
		return codex.ToolCallResult{}, errors.New("未知语音工具")
	}
}
