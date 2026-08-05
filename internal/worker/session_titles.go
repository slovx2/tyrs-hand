package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

const (
	sessionTitleModel    = "gpt-5.6-luna"
	sessionTitleTimeout  = 45 * time.Second
	sessionTitleMaxRunes = 36
)

func (c *HostDesktopController) runSessionTitleLoop(ctx context.Context) {
	for ctx.Err() == nil {
		claimCtx, cancel := context.WithTimeout(ctx, c.processor.cfg.ControlTimeout)
		claim, err := c.processor.client.ClaimSessionTitle(claimCtx)
		cancel()
		if err != nil {
			c.processor.logger.Warn("领取 Session 标题任务失败", zap.Error(err))
			if !waitContext(ctx, 3*time.Second) {
				return
			}
			continue
		}
		if claim.Task == nil {
			if !waitContext(ctx, 2*time.Second) {
				return
			}
			continue
		}
		task := claim.Task
		titleCtx, titleCancel := context.WithTimeout(ctx, sessionTitleTimeout)
		title, generateErr := c.generateSessionTitle(titleCtx, *task)
		titleCancel()
		ackCtx, ackCancel := context.WithTimeout(ctx, c.processor.cfg.ControlTimeout)
		if generateErr == nil {
			err = c.processor.client.CompleteSessionTitle(ackCtx, task.ID,
				workerprotocol.SessionTitleCompleteRequest{LeaseToken: task.LeaseToken,
					TitleRevision: task.TitleRevision, Title: title})
		} else {
			err = c.processor.client.FailSessionTitle(ackCtx, task.ID,
				workerprotocol.SessionTitleFailRequest{LeaseToken: task.LeaseToken,
					ErrorCode: sessionTitleErrorCode(generateErr)})
		}
		ackCancel()
		if err != nil {
			c.processor.logger.Warn("确认 Session 标题任务失败",
				zap.String("task_id", task.ID.String()), zap.Error(err))
		} else if generateErr != nil {
			c.processor.logger.Warn("生成 Session 标题失败，已进入重试",
				zap.String("task_id", task.ID.String()), zap.Error(generateErr))
		}
	}
}

func (c *HostDesktopController) generateSessionTitle(ctx context.Context,
	task workerprotocol.SessionTitleTask,
) (string, error) {
	client := c.workspace.client
	if client == nil {
		return "", errors.New("宿主 App Server 尚未连接")
	}
	base := filepath.Join(c.processor.cfg.WorkerDataRoot, "session-title-tasks")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	cwd, err := os.MkdirTemp(base, task.ID.String()+"-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(cwd) }()
	threadID, err := startSessionTitleThread(ctx, client, cwd)
	if err != nil {
		return "", err
	}
	subscription := client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	defer subscription.Close()
	turnID, err := startSessionTitleTurn(ctx, client, threadID, task.FirstMessage)
	if err != nil {
		return "", err
	}
	raw, err := waitSessionTitleTurn(ctx, subscription.Events(), threadID, turnID)
	if err != nil {
		return "", err
	}
	var output struct {
		Title string `json:"title"`
	}
	if json.Unmarshal([]byte(raw), &output) != nil {
		return "", errors.New("luna 标题不符合结构化输出")
	}
	title := normalizeGeneratedSessionTitle(output.Title)
	if title == "" {
		return "", errors.New("luna 标题为空")
	}
	return title, nil
}

type sessionTitleCaller interface {
	Call(context.Context, string, any, any) error
}

func startSessionTitleThread(ctx context.Context, client sessionTitleCaller, cwd string) (string, error) {
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	params := map[string]any{
		"model": sessionTitleModel, "cwd": cwd, "runtimeWorkspaceRoots": []string{cwd},
		"approvalPolicy": "never", "sandbox": "read-only", "ephemeral": true,
		"serviceTier": "fast", "dynamicTools": []any{},
		"baseInstructions":      "你是会话标题生成器。只根据用户提供的文本生成标题，不调用工具，不读取文件，不执行任何操作。",
		"developerInstructions": "输出一个简洁、具体、忠于原意的中文标题。不要添加解释。",
		"config": map[string]any{"model_reasoning_effort": "low", "service_tier": "fast",
			"default_tools_enabled": false, "features": map[string]any{"memories": false}},
	}
	if err := client.Call(ctx, "thread/start", params, &response); err != nil {
		return "", err
	}
	if response.Thread.ID == "" {
		return "", errors.New("luna thread/start 未返回 Thread ID")
	}
	return response.Thread.ID, nil
}

func startSessionTitleTurn(ctx context.Context, client sessionTitleCaller, threadID,
	message string,
) (string, error) {
	var response struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	outputSchema := map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"title"},
		"properties": map[string]any{"title": map[string]any{
			"type": "string", "minLength": 1, "maxLength": sessionTitleMaxRunes}},
	}
	params := map[string]any{
		"threadId": threadID, "model": sessionTitleModel, "effort": "low",
		"serviceTier": "fast", "outputSchema": outputSchema,
		"input": []map[string]any{{"type": "text", "text": message, "textElements": []any{}}},
	}
	if err := client.Call(ctx, "turn/start", params, &response); err != nil {
		return "", err
	}
	if response.Turn.ID == "" {
		return "", errors.New("luna turn/start 未返回 Turn ID")
	}
	return response.Turn.ID, nil
}

func waitSessionTitleTurn(ctx context.Context, events <-chan codex.Event, threadID, turnID string,
) (string, error) {
	var finalOutput string
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case event, ok := <-events:
			if !ok {
				return "", errors.New("luna 标题事件流已关闭")
			}
			if output, _ := finalOutputFromEvent(event); output != "" {
				finalOutput = output
			}
			if event.Method != "turn/completed" {
				continue
			}
			matched, status := completedTurn(event.Params, threadID, turnID)
			if !matched {
				continue
			}
			if status != "completed" {
				return "", fmt.Errorf("luna 标题 Turn 终态为 %s", status)
			}
			if finalOutput == "" {
				return "", errors.New("luna 标题 Turn 没有最终输出")
			}
			return finalOutput, nil
		}
	}
}

func normalizeGeneratedSessionTitle(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > sessionTitleMaxRunes {
		value = string(runes[:sessionTitleMaxRunes])
	}
	return strings.TrimSpace(value)
}

func sessionTitleErrorCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if strings.Contains(err.Error(), "结构化输出") || strings.Contains(err.Error(), "标题为空") {
		return "invalid_output"
	}
	return "generation_failed"
}
