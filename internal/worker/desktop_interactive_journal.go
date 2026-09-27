package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/interactiveprotocol"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

// 保存的是本地选择，不代表工具已经执行；真实结果仍以 Run Result 和原生历史为准。
type desktopInteractiveAnswer struct {
	Request   workerprotocol.InteractiveRegisterRequest `json:"request"`
	Answer    workerprotocol.InteractiveAnswerRequest   `json:"answer"`
	Delivered bool                                      `json:"delivered,omitempty"`
	Conflict  string                                    `json:"conflict,omitempty"`
	Remote    *workerprotocol.InteractiveState          `json:"remote,omitempty"`
}

func (c *desktopController) persistOfflineInteractive(ctx context.Context, request codex.ServerRequest,
	input workerprotocol.InteractiveAnswerRequest,
) (json.RawMessage, error) {
	normalized, err := normalizeOfflineAnswer(request.Method, request.Params, input.Answer)
	if err != nil {
		return nil, err
	}
	input.Answer = normalized
	coordinator := c.processor.coordinator
	if coordinator == nil || c.processor.journals == nil ||
		input.AppServerGeneration < 1 || !interactiveprotocol.ValidRequestID(input.RequestID) {
		return nil, errors.New("离线审批缺少可持久化的活动回合")
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	// 优先精确匹配回合；回合 ID 尚未观察到或 elicitation 不带回合时，只接受同一 Thread 唯一活动回合。
	var exact, loose []*runJournal
	for _, active := range coordinator.active {
		if active.engine != c.processor.runtimeIdentity.Engine || active.workspace != input.WorkspaceID ||
			active.thread != input.ThreadID {
			continue
		}
		if input.TurnID != "" && active.turnID == input.TurnID {
			exact = append(exact, active.journal)
		}
		if input.TurnID == "" || active.turnID == "" {
			loose = append(loose, active.journal)
		}
	}
	var journal *runJournal
	switch {
	case len(exact) == 1:
		journal = exact[0]
	case len(exact) == 0 && len(loose) == 1:
		journal = loose[0]
	default:
		return nil, errors.New("离线审批未找到唯一匹配的活动回合")
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if journal.ControlAbandoned || journal.Result != nil || journal.Failure != "" ||
		journal.AppServerGeneration != input.AppServerGeneration ||
		c.workspace.currentGeneration() != input.AppServerGeneration {
		return nil, errors.New("离线审批所属回合或运行代次已失效")
	}
	entry := desktopInteractiveAnswer{Request: workerprotocol.InteractiveRegisterRequest{
		Method: request.Method, RequestID: request.ID, Params: request.Params,
		AppServerGeneration: input.AppServerGeneration}, Answer: input}
	for _, existing := range journal.InteractiveAnswers {
		if jsonSemanticallyEqual(existing.Answer.RequestID, input.RequestID) {
			if reflect.DeepEqual(existing.Request, entry.Request) && reflect.DeepEqual(existing.Answer, entry.Answer) {
				return normalized, nil
			}
			return nil, errors.New("同一离线审批不能接受不同答案")
		}
	}
	journal.InteractiveAnswers = append(journal.InteractiveAnswers, entry)
	if err := c.processor.journals.save(journal); err != nil {
		journal.InteractiveAnswers = journal.InteractiveAnswers[:len(journal.InteractiveAnswers)-1]
		return nil, fmt.Errorf("持久化离线审批: %w", err)
	}
	return normalized, nil
}

func normalizeOfflineAnswer(method string, params, answer json.RawMessage) (json.RawMessage, error) {
	if method != interactiveprotocol.UserInput {
		return interactiveprotocol.NormalizeAnswer(method, params, answer)
	}
	var value struct {
		Answers map[string]struct {
			Answers []string `json:"answers"`
		} `json:"answers"`
	}
	if json.Unmarshal(answer, &value) != nil || value.Answers == nil {
		return nil, errors.New("交互回答参数无效")
	}
	var request struct {
		Questions []struct {
			ID string `json:"id"`
		} `json:"questions"`
	}
	if json.Unmarshal(params, &request) != nil || len(request.Questions) == 0 {
		return nil, errors.New("交互请求缺少原始问题")
	}
	questions := make(map[string]bool, len(request.Questions))
	for _, question := range request.Questions {
		if question.ID == "" || questions[question.ID] {
			return nil, errors.New("交互请求问题 ID 无效")
		}
		questions[question.ID] = true
	}
	for id, entry := range value.Answers {
		if !questions[id] {
			return nil, errors.New("交互回答包含原始请求以外的问题")
		}
		if len(entry.Answers) == 0 {
			return nil, errors.New("交互回答不能为空")
		}
		for _, item := range entry.Answers {
			if strings.TrimSpace(item) == "" {
				return nil, errors.New("交互回答不能为空")
			}
		}
	}
	return json.Marshal(value)
}

func jsonSemanticallyEqual(left, right json.RawMessage) bool {
	var a, b any
	leftDecoder, rightDecoder := json.NewDecoder(bytes.NewReader(left)), json.NewDecoder(bytes.NewReader(right))
	leftDecoder.UseNumber()
	rightDecoder.UseNumber()
	return json.Valid(left) && json.Valid(right) && leftDecoder.Decode(&a) == nil &&
		rightDecoder.Decode(&b) == nil && reflect.DeepEqual(a, b)
}

// 离线答案与 Control 状态不一致时追加的事件类型。SSH 端答案已经真实生效，
// 冲突只作记录与通知，不回滚、不改判本地结果。
const offlineInteractiveConflictEvent = "interactive.offline_conflict"

// 调用方持有 journal.mu。补报只访问 Control，绝不重新提交原生工具或模型请求。
// 冲突不阻断补报：记录冲突事件后继续，由调用方照常上传事件并提交真实终态。
func flushDesktopInteractiveLocked(ctx context.Context, client *workerprotocol.Client,
	store *journalStore, journal *runJournal, timeout time.Duration,
) error {
	for index := range journal.InteractiveAnswers {
		entry := &journal.InteractiveAnswers[index]
		if entry.Delivered || entry.Conflict != "" {
			continue
		}
		requestCtx, cancel := context.WithTimeout(ctx, timeout)
		_, err := client.RegisterInteractive(requestCtx, &journal.Task, entry.Request.Method,
			entry.Request.RequestID, entry.Request.Params, entry.Request.AppServerGeneration)
		cancel()
		if err != nil {
			if !definitiveInteractiveError(err) {
				return err
			}
			if err := recordInteractiveConflictLocked(store, journal, entry,
				fmt.Sprintf("control_rejected_%d", controlHTTPStatus(err)), nil); err != nil {
				return err
			}
			continue
		}
		requestCtx, cancel = context.WithTimeout(ctx, timeout)
		state, err := client.AnswerInteractive(requestCtx, entry.Answer)
		cancel()
		if err != nil {
			if !definitiveInteractiveError(err) {
				return err
			}
			if err := recordInteractiveConflictLocked(store, journal, entry,
				fmt.Sprintf("control_rejected_%d", controlHTTPStatus(err)), nil); err != nil {
				return err
			}
			continue
		}
		// HTTP 200 也可能是拒绝；仅相同原生身份、已解决且相同答案可视为幂等确认。
		normalized, normalizeErr := normalizeOfflineAnswer(entry.Request.Method, entry.Request.Params, state.Answer)
		if state.Status != "resolved" || state.Method != entry.Request.Method ||
			state.AppServerGeneration != entry.Request.AppServerGeneration ||
			!jsonSemanticallyEqual(state.RequestID, entry.Request.RequestID) || normalizeErr != nil ||
			!jsonSemanticallyEqual(normalized, entry.Answer.Answer) {
			if err := recordInteractiveConflictLocked(store, journal, entry,
				"control_answer_mismatch", &state); err != nil {
				return err
			}
			continue
		}
		entry.Delivered = true
		if err := store.save(journal); err != nil {
			entry.Delivered = false
			return err
		}
	}
	return nil
}

// 冲突事件与本地答案原子落盘；重启后不会再次补报同一答案，也不会重复追加事件。
func recordInteractiveConflictLocked(store *journalStore, journal *runJournal,
	entry *desktopInteractiveAnswer, conflict string, remote *workerprotocol.InteractiveState,
) error {
	payload := map[string]any{
		"requestId": entry.Request.RequestID, "method": entry.Request.Method,
		"threadId": entry.Answer.ThreadID, "turnId": entry.Answer.TurnID,
		"conflict": conflict, "localSurface": entry.Answer.Surface,
	}
	// 问答和 elicitation 可能含 Secret 或自由文本，只记录审批类的决定本身。
	disclose := entry.Request.Method != interactiveprotocol.UserInput &&
		entry.Request.Method != interactiveprotocol.MCPElicitation
	if disclose {
		payload["localAnswer"] = entry.Answer.Answer
	}
	if remote != nil {
		remoteView := map[string]any{"status": remote.Status, "surface": remote.Surface}
		if disclose && len(remote.Answer) > 0 {
			remoteView["answer"] = remote.Answer
		}
		payload["remote"] = remoteView
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	previousEvents, previousSequence := len(journal.PendingEvents), journal.NextSequence
	entry.Conflict, entry.Remote = conflict, remote
	journal.PendingEvents = append(journal.PendingEvents, workerprotocol.EventInput{
		Sequence: journal.NextSequence, Type: offlineInteractiveConflictEvent, Payload: raw,
	})
	journal.NextSequence++
	if err := store.save(journal); err != nil {
		entry.Conflict, entry.Remote = "", nil
		journal.PendingEvents = journal.PendingEvents[:previousEvents]
		journal.NextSequence = previousSequence
		return err
	}
	return nil
}
