package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

// 只对已经回退的线程等待 Control；普通本地回合不增加网络前置依赖。
// 单独持久化使 Worker 重启后仍能消费同一个 replacement。
type desktopRollbackJournal struct {
	State   workerprotocol.DesktopRollbackState `json:"state"`
	Applied bool                                `json:"applied"`
	Started bool                                `json:"started"`
}

func (s *journalStore) desktopRollbackPath(threadID string) string {
	digest := sha256.Sum256([]byte(threadID))
	return filepath.Join(filepath.Dir(s.directory), "desktop-rollbacks", hex.EncodeToString(digest[:])+".json")
}

func (s *journalStore) saveDesktopRollback(journal desktopRollbackJournal) error {
	if s == nil {
		return errors.New("回退缺少持久化 Journal")
	}
	path := s.desktopRollbackPath(journal.State.ThreadID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	return writeJournalFile(filepath.Dir(path), path, body)
}

func (s *journalStore) loadDesktopRollback(threadID string) (*desktopRollbackJournal, error) {
	if s == nil {
		return nil, nil
	}
	body, err := os.ReadFile(s.desktopRollbackPath(threadID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var journal desktopRollbackJournal
	if err := json.Unmarshal(body, &journal); err != nil {
		return nil, err
	}
	if journal.State.ThreadID != threadID || journal.State.TargetTurnID == "" {
		return nil, errors.New("回退 Journal 的目标不匹配")
	}
	return &journal, nil
}

func (s *journalStore) removeDesktopRollback(threadID string) error {
	path := s.desktopRollbackPath(threadID)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

// 原生响应丢失时只读对账；不重发回退，不把缺失的 turns 当成空历史。
func readDesktopRollbackApplied(ctx context.Context, client codex.RuntimeClient, state workerprotocol.DesktopRollbackState) (json.RawMessage, bool) {
	var raw json.RawMessage
	if err := client.Call(ctx, "thread/read", map[string]any{"threadId": state.ThreadID, "includeTurns": true}, &raw); err != nil {
		return nil, false
	}
	var result struct {
		Thread struct {
			ID    string
			Turns *[]struct{ ID string }
		}
	}
	if json.Unmarshal(raw, &result) != nil || result.Thread.ID != state.ThreadID || result.Thread.Turns == nil {
		return nil, false
	}
	for _, turn := range *result.Thread.Turns {
		if turn.ID == state.TargetTurnID {
			return nil, false
		}
	}
	return raw, true
}

func (c *desktopController) completeDesktopRollback(state workerprotocol.DesktopRollbackState, result json.RawMessage, cause error) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(c.processor.workspaces.ctx, c.controlTimeout())
	defer cancel()
	var unknown *codex.RequestError
	reconciled := errors.As(cause, &unknown) && unknown.State == codex.RequestUnknown
	if reconciled {
		client := c.workspace.currentClient()
		if client == nil {
			return result, cause
		}
		var applied bool
		result, applied = readDesktopRollbackApplied(ctx, client, state)
		if !applied {
			return result, cause
		}
	}
	request := workerprotocol.DesktopRollbackCompleteRequest{WorkspaceID: state.WorkspaceID, Response: result}
	if cause != nil && !reconciled {
		request.Error = cause.Error()
	}
	if err := c.processor.client.CompleteDesktopRollback(ctx, state.ID, request); err != nil {
		return result, err
	}
	if request.Error != "" {
		if err := c.processor.journals.removeDesktopRollback(state.ThreadID); err != nil {
			return result, err
		}
		return result, cause
	}
	if err := c.processor.journals.saveDesktopRollback(desktopRollbackJournal{State: state, Applied: true}); err != nil {
		return result, err
	}
	if reconciled {
		data, _ := json.Marshal(map[string]any{"threadId": state.ThreadID, "beforeTurnId": state.TargetTurnID, "applied": true})
		return nil, &appserverhub.ProtocolError{Code: -32053,
			Message: "回退已对账完成，但原生响应丢失；请重新读取会话，不要重复回退", Data: data}
	}
	return result, nil
}

func (c *desktopController) preflightDesktopReplacement(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	threadID, _ := callScope(params)
	journal, err := c.processor.journals.loadDesktopRollback(threadID)
	if err != nil || journal == nil {
		return params, err
	}
	if !c.controlEnabled() || journal.State.WorkspaceID != c.workspace.runtime.WorkspaceID {
		return nil, errors.New("该线程有待处理的回退，需要原 Control 绑定完成对账")
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.controlTimeout())
	defer cancel()
	if !journal.Applied {
		client := c.workspace.currentClient()
		if client == nil {
			return nil, errors.New("回退结果尚未确认，Runtime 正在恢复")
		}
		raw, applied := readDesktopRollbackApplied(requestCtx, client, journal.State)
		if !applied {
			return nil, errors.New("回退结果尚未确认，不能开始替换回合")
		}
		if _, err := c.completeDesktopRollback(journal.State, raw, nil); err != nil {
			return nil, err
		}
		journal.Applied = true
	}
	preflight, err := c.processor.client.PreflightDesktopTurn(requestCtx, workerprotocol.DesktopTurnPreflightRequest{
		WorkspaceID: journal.State.WorkspaceID, Params: params,
	})
	if err != nil {
		return nil, err
	}
	var prepared struct{ ClientUserMessageID string }
	if json.Unmarshal(preflight.Params, &prepared) != nil {
		return nil, errors.New("收到无效的 Control replacement 输入")
	}
	if prepared.ClientUserMessageID != journal.State.ID.String() {
		return preflight.Params, c.processor.journals.removeDesktopRollback(threadID)
	}
	if journal.Started {
		return nil, fmt.Errorf("替换回合已提交，等待 Control 确认：%s", journal.State.ID)
	}
	return preflight.Params, nil
}

func (c *desktopController) startDesktopReplacement(params json.RawMessage) error {
	c.workspace.rollbackMu.Lock()
	defer c.workspace.rollbackMu.Unlock()
	threadID, _ := callScope(params)
	journal, err := c.processor.journals.loadDesktopRollback(threadID)
	if err != nil || journal == nil {
		return err
	}
	var prepared struct{ ClientUserMessageID string }
	if json.Unmarshal(params, &prepared) != nil || prepared.ClientUserMessageID != journal.State.ID.String() {
		return errors.New("回退后的输入缺少 Control 预留标识")
	}
	if journal.Started {
		return errors.New("替换回合已提交，不能重复执行")
	}
	journal.Started = true
	return c.processor.journals.saveDesktopRollback(*journal)
}
