package appserverhub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/codex"
)

// 在 Control 预留之前解析原生目标，legacy 或无效请求不会留下 replacement。
func (r *Hub) prepareThreadRevert(ctx context.Context, method string, raw json.RawMessage) (json.RawMessage, error) {
	var params struct {
		ThreadID     string `json:"threadId"`
		BeforeTurnID string `json:"beforeTurnId"`
		NumTurns     int    `json:"numTurns"`
	}
	if json.Unmarshal(raw, &params) != nil || strings.TrimSpace(params.ThreadID) == "" {
		return nil, &ProtocolError{Code: -32602, Message: "回退参数或 threadId 无效"}
	}
	if method == "thread/rollback" {
		if params.NumTurns < 1 {
			return nil, &ProtocolError{Code: -32602, Message: "numTurns 必须是正整数"}
		}
		resolved, err := codex.ResolveThreadRollback(ctx, r.upstream, params.ThreadID, params.NumTurns)
		if err != nil {
			return nil, err
		}
		return json.Marshal(resolved)
	}
	if strings.TrimSpace(params.BeforeTurnID) == "" {
		return nil, &ProtocolError{Code: -32602, Message: "beforeTurnId 必须是非空字符串"}
	}
	if err := codex.RequirePaginatedThread(ctx, r.upstream, params.ThreadID); err != nil {
		return nil, err
	}
	return raw, nil
}

// 旧 Desktop 仍需要完整 turns；只从原生分页补齐响应，不构造或改写历史。
func (r *Hub) hydrateRollbackResult(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var result struct {
		Thread map[string]json.RawMessage `json:"thread"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	var threadID string
	if json.Unmarshal(result.Thread["id"], &threadID) != nil || threadID == "" {
		return nil, errors.New("原生 revert 响应缺少 Thread ID")
	}
	turns := make([]json.RawMessage, 0)
	var cursor *string
	seen := make(map[string]bool)
	for {
		var page struct {
			Data       []json.RawMessage `json:"data"`
			NextCursor *string           `json:"nextCursor"`
		}
		if err := r.upstream.Call(ctx, "thread/turns/list", map[string]any{
			"threadId": threadID, "cursor": cursor, "limit": 100,
			"sortDirection": "asc", "itemsView": "full",
		}, &page); err != nil {
			return nil, err
		}
		turns = append(turns, page.Data...)
		if page.NextCursor == nil {
			break
		}
		if *page.NextCursor == "" || seen[*page.NextCursor] || len(page.Data) == 0 {
			return nil, errors.New("原生回退历史分页没有推进")
		}
		seen[*page.NextCursor] = true
		cursor = page.NextCursor
	}
	result.Thread["turns"], _ = json.Marshal(turns)
	return json.Marshal(result)
}
