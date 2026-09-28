package worker

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

// Runner 加载 Run Journal 前对账崩溃窗口。只读取已登记线程，不加载线程或重新提交输入。
func (c *HostDesktopController) recoverNativeQueueJournals(ctx context.Context) error {
	integration, runtime := c.snapshot()
	store := c.processor.journals
	if store == nil {
		return nil
	}
	if runtime.Info().Engine != c.processor.client.Engine() {
		return errors.New("恢复队列的运行时与 Worker 引擎不一致")
	}
	entries, err := store.loadQueues()
	if err != nil || len(entries) == 0 {
		return err
	}
	storedRuns, err := store.loadAll()
	if err != nil {
		return err
	}
	runs := make(map[uuid.UUID]*runJournal, len(storedRuns))
	for _, run := range storedRuns {
		runs[run.Task.Claimed.RunID] = run
	}
	histories := make(map[string]codex.ThreadSnapshot)
	for _, entry := range entries {
		if entry.Engine != runtime.Info().Engine {
			return errors.New("恢复队列的 Journal 与运行时引擎不一致")
		}
		if err := validateQueueAdmission(entry); err != nil {
			return err
		}
		var run *runJournal
		if entry.Task != nil {
			run = runs[entry.Task.Claimed.RunID]
		}
		if run != nil && run.DesktopRequest != nil && run.Task.Claimed.ConfirmedTurnID != "" &&
			(run.TerminalDelivered || run.Result != nil || run.Failure != "" || run.ControlAbandoned) {
			if run.Task.Claimed.ExternalThreadID != entry.ThreadID || run.Task.Snapshot.Runtime.Engine != entry.Engine {
				return errors.New("队列与 Run Journal 的原生身份不一致")
			}
			if err := store.removeQueue(entry.ThreadID, entry.ClientID); err != nil {
				return err
			}
			continue
		}
		history, found := histories[entry.ThreadID]
		if !found {
			history, err = codex.NewRuntime(runtime.Client()).ReadThread(ctx, entry.ThreadID)
			if err != nil {
				if run != nil {
					run.ControlAbandoned = true
					if err := store.save(run); err != nil {
						return err
					}
				}
				c.processor.logger.Warn("队列历史尚不可核实，保留入队身份且不重放", zap.String("thread_id", entry.ThreadID), zap.Error(err))
				continue
			}
			histories[entry.ThreadID] = history
		}
		turn, found := history.TurnByClientID(entry.ClientID)
		if !found {
			if run != nil {
				run.ControlAbandoned = true
				if err := store.save(run); err != nil {
					return err
				}
			}
			continue // 尚在原生队列或执行结果未知，不能自行 resume/start。
		}
		if entry.TurnID != "" && entry.TurnID != turn.ID {
			return errors.New("队列 Journal 与官方历史回合不一致")
		}
		var input json.RawMessage
		for _, item := range turn.Items {
			if item.Type == "userMessage" && item.ClientID == entry.ClientID {
				input = item.Content
				break
			}
		}
		entry, err = entry.withInput(input)
		if err != nil {
			return err
		}
		entry.TurnID = turn.ID
		if err := store.saveQueue(entry); err != nil {
			return err
		}
		if entry.Task == nil {
			if !isActiveCodexTurnStatus(turn.Status) {
				if err := store.removeQueue(entry.ThreadID, entry.ClientID); err != nil {
					return err
				}
			}
			continue
		}
		if run == nil {
			run = &runJournal{NextSequence: 1}
		}
		run.Task = *entry.Task
		run.Task.Claimed.SubmissionID, run.Task.Claimed.ConfirmedTurnID = turn.ID, turn.ID
		run.AppServerGeneration = runtime.Generation()
		result, _ := json.Marshal(map[string]any{"turn": map[string]string{"id": turn.ID}})
		run.DesktopRequest = &workerprotocol.DesktopTurnPrepareRequest{
			WorkspaceID: entry.Manifest.WorkspaceID, RunID: entry.Task.Claimed.RunID,
			IntentID: entry.Task.Claimed.ID, TurnID: turn.ID, Params: entry.Params,
			RequestKey: desktopRequestKey("thread/queue/add", entry.Params, result),
		}
		// 未确认终态或原绑定已改变时只保留证据，不将旧任务归属到新 Workspace。
		run.ControlAbandoned = isActiveCodexTurnStatus(turn.Status) || integration == nil ||
			!reflect.DeepEqual(integration.workspace.manifest, *entry.Manifest)
		if turn.Status == "completed" {
			answer, outputType := turn.FinalOutput()
			value, resultErr := remoteCompletedResult(answer, outputType, turn.ID, 0, "thread/read")
			if resultErr == nil {
				run.Result = &value
			} else {
				run.FailureCode, run.Failure = "worker_error", resultErr.Error()
			}
		} else if !isActiveCodexTurnStatus(turn.Status) {
			cause := remoteTurnTerminalError("恢复队列快照", turn.Status)
			run.FailureCode, run.Failure = "worker_error", cause.Error()
			if errors.Is(cause, errRemoteInterrupt) {
				run.FailureCode = "user_interrupt"
			}
			run.CodexError = codexErrorFromSnapshot(entry.ThreadID, turn.ID, turn.Error)
		}
		if err := store.save(run); err != nil {
			return err
		}
		if err := store.removeQueue(entry.ThreadID, entry.ClientID); err != nil {
			return err
		}
	}
	return nil
}

func validateQueueAdmission(entry *hostQueueJournal) error {
	if entry == nil {
		return errors.New("队列 Journal 缺少记录")
	}
	if entry.Task == nil && entry.Manifest == nil {
		return nil
	}
	if entry.Task == nil || entry.Manifest == nil || entry.Manifest.WorkspaceID == uuid.Nil ||
		entry.Task.Claimed.ID == uuid.Nil || entry.Task.Claimed.RunID == uuid.Nil ||
		entry.Task.Claimed.ExternalThreadID != entry.ThreadID || entry.Task.Snapshot.Runtime.Engine != entry.Engine ||
		entry.Task.Snapshot.Session == nil || entry.Task.Snapshot.Session.Project == nil ||
		entry.Task.Snapshot.Session.Project.WorkspaceID != entry.Manifest.WorkspaceID {
		return errors.New("队列入队身份快照不完整或跨越 Workspace")
	}
	return nil
}
