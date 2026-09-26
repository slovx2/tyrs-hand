package appserverhub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

type threadItemsQuery struct {
	ThreadID      string
	TurnID        *string
	Cursor        *string
	Limit         *uint32
	SortDirection *string
}

type threadItemEntry struct {
	TurnID string          `json:"turnId"`
	Item   json.RawMessage `json:"item"`
}

type threadItemsCursor struct {
	Version                                         int
	ThreadID, TurnFilter, Direction, TurnID, ItemID string
	Inclusive                                       bool
}

// 只补齐已验证的 Codex 入口；Claude 继续使用原生适配器的实现。
func (r *Hub) usesCodexItemHistory() bool {
	if r.options.RuntimeInfo == nil {
		return false
	}
	body, err := marshalRaw(r.options.RuntimeInfo())
	if err != nil {
		return false
	}
	var identity struct{ Engine string }
	return json.Unmarshal(body, &identity) == nil && identity.Engine == "codex"
}

func invalidThreadItems(message string) error {
	return &ProtocolError{Code: -32602, Message: message}
}

func decodeThreadItemsQuery(raw json.RawMessage) (threadItemsQuery, *threadItemsCursor, error) {
	var query threadItemsQuery
	if err := json.Unmarshal(raw, &query); err != nil || strings.TrimSpace(query.ThreadID) == "" {
		return query, nil, invalidThreadItems("thread/items/list 参数或 threadId 无效")
	}
	if query.TurnID != nil && strings.TrimSpace(*query.TurnID) == "" {
		return query, nil, invalidThreadItems("turnId 必须是非空字符串")
	}
	if query.Limit != nil && (*query.Limit < 1 || *query.Limit > 1000) {
		return query, nil, invalidThreadItems("limit 必须在 1 到 1000 之间")
	}
	if query.SortDirection == nil {
		direction := "asc"
		query.SortDirection = &direction
	}
	if *query.SortDirection != "asc" && *query.SortDirection != "desc" {
		return query, nil, invalidThreadItems("sortDirection 无效")
	}
	if query.Cursor == nil {
		return query, nil, nil
	}
	if len(*query.Cursor) > 4096 {
		return query, nil, invalidThreadItems("历史游标无效")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(*query.Cursor)
	if err != nil {
		return query, nil, invalidThreadItems("历史游标无效")
	}
	var cursor threadItemsCursor
	if json.Unmarshal(decoded, &cursor) != nil || cursor.Version != 1 || cursor.TurnID == "" || cursor.ItemID == "" ||
		cursor.ThreadID != query.ThreadID || cursor.TurnFilter != query.turnFilter() || cursor.Direction != *query.SortDirection {
		return query, nil, invalidThreadItems("历史游标不属于本次查询")
	}
	return query, &cursor, nil
}

func (q threadItemsQuery) turnFilter() string {
	if q.TurnID == nil {
		return ""
	}
	return *q.TurnID
}

// Codex 0.147.0 的 thread/items/list 明确返回 -32601；从原生完整 Turn 历史分页读取，
// 保留每个原始 item，不创建会话、Turn、模型请求或任何预制历史。
func (r *Hub) listNativeThreadItems(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	query, cursor, err := decodeThreadItemsQuery(raw)
	if err != nil {
		return nil, err
	}
	entries := make([]threadItemEntry, 0)
	var next *string
	seenCursors := map[string]bool{}
	seenTurns := map[string]bool{}
	for {
		params := map[string]any{"threadId": query.ThreadID, "cursor": next, "limit": 100, "sortDirection": "asc", "itemsView": "full"}
		var page struct {
			Data []struct {
				ID    string
				Items []json.RawMessage
			}
			NextCursor *string
		}
		if err := r.upstream.Call(ctx, "thread/turns/list", params, &page); err != nil {
			return nil, err
		}
		for _, turn := range page.Data {
			if turn.ID == "" || seenTurns[turn.ID] {
				return nil, fmt.Errorf("原生历史返回空或重复 Turn ID")
			}
			seenTurns[turn.ID] = true
			if query.TurnID != nil && turn.ID != *query.TurnID {
				continue
			}
			for _, item := range turn.Items {
				entries = append(entries, threadItemEntry{TurnID: turn.ID, Item: item})
			}
		}
		next = page.NextCursor
		if next == nil {
			break
		}
		if *next == "" || seenCursors[*next] {
			return nil, fmt.Errorf("原生历史分页游标没有推进")
		}
		seenCursors[*next] = true
	}
	return paginateThreadItems(query, cursor, entries)
}

func threadItemIdentity(entry threadItemEntry) (string, error) {
	var item struct{ ID string }
	if err := json.Unmarshal(entry.Item, &item); err != nil || item.ID == "" {
		return "", fmt.Errorf("原生历史条目缺少有效 ID")
	}
	return item.ID, nil
}

func paginateThreadItems(query threadItemsQuery, cursor *threadItemsCursor, entries []threadItemEntry) (json.RawMessage, error) {
	if *query.SortDirection == "desc" {
		slices.Reverse(entries)
	}
	offset := 0
	if cursor != nil {
		offset = -1
		for index, entry := range entries {
			id, err := threadItemIdentity(entry)
			if err != nil {
				return nil, err
			}
			if entry.TurnID == cursor.TurnID && id == cursor.ItemID {
				offset = index
				if !cursor.Inclusive {
					offset++
				}
				break
			}
		}
		if offset < 0 {
			return nil, invalidThreadItems("历史游标已失效，锚点条目不存在")
		}
	}
	limit := 50
	if query.Limit != nil {
		limit = int(*query.Limit)
	}
	end := min(offset+limit, len(entries))
	data := make([]threadItemEntry, end-offset)
	copy(data, entries[offset:end])
	cursorFor := func(entry threadItemEntry, direction string, inclusive bool) (string, error) {
		id, err := threadItemIdentity(entry)
		if err != nil {
			return "", err
		}
		body, err := json.Marshal(threadItemsCursor{Version: 1, ThreadID: query.ThreadID, TurnFilter: query.turnFilter(), Direction: direction, TurnID: entry.TurnID, ItemID: id, Inclusive: inclusive})
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(body), nil
	}
	var nextCursor, backwardsCursor *string
	if len(data) > 0 {
		if end < len(entries) {
			next, err := cursorFor(data[len(data)-1], *query.SortDirection, false)
			if err != nil {
				return nil, err
			}
			nextCursor = &next
		}
		opposite := "desc"
		if *query.SortDirection == "desc" {
			opposite = "asc"
		}
		backwards, err := cursorFor(data[0], opposite, true)
		if err != nil {
			return nil, err
		}
		backwardsCursor = &backwards
	}
	return json.Marshal(map[string]any{"data": data, "nextCursor": nextCursor, "backwardsCursor": backwardsCursor})
}
