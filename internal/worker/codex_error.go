package worker

import (
	"encoding/json"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

func codexErrorFromEvent(event codex.Event) (*workerprotocol.CodexTurnError, bool) {
	if event.Method != "error" {
		return nil, false
	}
	var payload struct {
		Error struct {
			Message           string          `json:"message"`
			CodexErrorInfo    json.RawMessage `json:"codexErrorInfo"`
			AdditionalDetails string          `json:"additionalDetails"`
		} `json:"error"`
		WillRetry bool   `json:"willRetry"`
		ThreadID  string `json:"threadId"`
		TurnID    string `json:"turnId"`
	}
	if err := json.Unmarshal(event.Params, &payload); err != nil ||
		strings.TrimSpace(payload.Error.Message) == "" {
		return nil, false
	}
	return &workerprotocol.CodexTurnError{
		Message: payload.Error.Message, CodexErrorInfo: payload.Error.CodexErrorInfo,
		AdditionalDetails: payload.Error.AdditionalDetails, WillRetry: payload.WillRetry,
		ThreadID: payload.ThreadID, TurnID: payload.TurnID,
	}, true
}

func codexErrorFromSnapshot(threadID, turnID string, snapshot *codex.TurnError,
) *workerprotocol.CodexTurnError {
	if snapshot == nil || strings.TrimSpace(snapshot.Message) == "" {
		return nil
	}
	return &workerprotocol.CodexTurnError{
		Message: snapshot.Message, CodexErrorInfo: snapshot.CodexErrorInfo,
		AdditionalDetails: snapshot.AdditionalDetails, WillRetry: false,
		ThreadID: threadID, TurnID: turnID,
	}
}

func codexErrorFromCompletedEvent(raw json.RawMessage, threadID, turnID string) *workerprotocol.CodexTurnError {
	var payload struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID     string           `json:"id"`
			Status string           `json:"status"`
			Error  *codex.TurnError `json:"error,omitempty"`
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.ThreadID != threadID ||
		payload.Turn.ID != turnID || payload.Turn.Status == "completed" {
		return nil
	}
	return codexErrorFromSnapshot(threadID, turnID, payload.Turn.Error)
}
