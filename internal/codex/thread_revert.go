package codex

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ThreadRevertParams 始终锚定明确的原生 Turn，避免重试时按数量删除新的回合。
type ThreadRevertParams struct {
	ThreadID     string `json:"threadId"`
	BeforeTurnID string `json:"beforeTurnId"`
}

// ResolveThreadRollback 把旧客户端的数量语义解析为原生 paginated 回退锚点。
// legacy 没有原生 revert，必须明确拒绝，不能用 fork 或改写 rollout 模拟。
func ResolveThreadRollback(ctx context.Context, client RuntimeClient, threadID string, numTurns int) (ThreadRevertParams, error) {
	params := ThreadRevertParams{ThreadID: threadID}
	if strings.TrimSpace(threadID) == "" || numTurns < 1 {
		return params, errors.New("thread/rollback 需要非空 threadId 和正整数 numTurns")
	}
	if err := RequirePaginatedThread(ctx, client, threadID); err != nil {
		return params, err
	}
	var cursor *string
	seen := make(map[string]bool)
	remaining := numTurns
	for {
		var page struct {
			Data       []struct{ ID string } `json:"data"`
			NextCursor *string               `json:"nextCursor"`
		}
		if err := client.Call(ctx, "thread/turns/list", map[string]any{
			"threadId": threadID, "cursor": cursor, "limit": min(remaining, 100),
			"sortDirection": "desc", "itemsView": "notLoaded",
		}, &page); err != nil {
			return params, err
		}
		for _, turn := range page.Data {
			if turn.ID == "" || seen[turn.ID] {
				return params, errors.New("原生回退历史包含空或重复 Turn ID")
			}
			seen[turn.ID] = true
			remaining--
			params.BeforeTurnID = turn.ID
			if remaining == 0 {
				return params, nil
			}
		}
		if page.NextCursor == nil {
			return params, errors.New("thread/rollback 回退数量超过已有回合数")
		}
		if *page.NextCursor == "" || (cursor != nil && *cursor == *page.NextCursor) || len(page.Data) == 0 {
			return params, errors.New("原生回退历史分页没有推进")
		}
		cursor = page.NextCursor
	}
}

func RequirePaginatedThread(ctx context.Context, client RuntimeClient, threadID string) error {
	var result struct {
		Thread struct {
			ID          string `json:"id"`
			HistoryMode string `json:"historyMode"`
		} `json:"thread"`
	}
	if err := client.Call(ctx, "thread/read", map[string]any{
		"threadId": threadID, "includeTurns": false,
	}, &result); err != nil {
		return err
	}
	if result.Thread.ID != threadID {
		return errors.New("原生回退元数据的 Thread ID 不匹配")
	}
	if result.Thread.HistoryMode != "paginated" {
		return fmt.Errorf("原生 Codex 仅支持 paginated 线程回退，当前 historyMode=%q；legacy 线程不支持回退", result.Thread.HistoryMode)
	}
	return nil
}
