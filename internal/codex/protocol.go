package codex

import (
	"encoding/json"
	"fmt"
)

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("%d %s", e.Code, e.Message) }

type rpcError = RPCError

type requestEnvelope struct {
	ID     int64  `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

type notificationEnvelope struct {
	Method string `json:"method"`
	Params any    `json:"params"`
}

type responseEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Result any             `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type ToolCallRequest struct {
	ThreadID  string          `json:"threadId"`
	TurnID    string          `json:"turnId"`
	CallID    string          `json:"callId"`
	Namespace *string         `json:"namespace"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolCallResult struct {
	ContentItems []ToolContentItem `json:"contentItems"`
	Success      bool              `json:"success"`
}

type ToolContentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"imageUrl,omitempty"`
}

func TextToolResult(text string, success bool) ToolCallResult {
	return ToolCallResult{ContentItems: []ToolContentItem{{Type: "inputText", Text: text}}, Success: success}
}

type Event struct {
	Method string
	Params json.RawMessage
}
