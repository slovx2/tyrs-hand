package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

const remoteEventFlushInterval = time.Second

func (r *runtimeExecutor) runJournal(ctx context.Context, journal *runJournal,
	commands chan workerprotocol.RunCommand, slots chan struct{}, active *sync.WaitGroup,
) {
	defer active.Done()
	slotReleased := false
	releaseSlot := func() {
		if !slotReleased {
			<-slots
			slotReleased = true
		}
	}
	defer func() {
		releaseSlot()
		// 释放并发槽后立刻重新检查待办，避免等下一次唤醒。
		r.claimWake.Notify([]string{workerprotocol.WakeClaim})
	}()
	task := &journal.Task
	logger := r.logger.With(zap.String("run_id", task.Claimed.RunID.String()),
		zap.String("intent_id", task.Claimed.ID.String()))
	if journal.ControlAbandoned {
		return
	}
	if journal.TerminalDelivered {
		releaseSlot()
		r.deliverTerminal(ctx, journal, logger)
		return
	}

	defer r.releaseRun(journal)
	if task.Claimed.SubmissionID != "" || task.Claimed.ConfirmedTurnID != "" {
		task.Claimed.Recovering = true
	}
	if err := r.journals.save(journal); err != nil {
		logger.Error("持久化恢复后的 Run 状态失败", zap.Error(err))
		return
	}
	if journal.Result != nil || journal.Failure != "" {
		r.releaseRun(journal)
		releaseSlot()
		r.deliverTerminal(ctx, journal, logger)
		return
	}
	if journal.DesktopRequest != nil {
		// 重启丢失了首次提交的内存屏障。完成持久化身份的补登记后才允许恢复观察和上传事件。
		for {
			err := r.syncRunState(ctx, journal, commands, logger)
			if err == nil {
				break
			}
			if !retryableControlError(err) {
				abandonRunJournal(r.journals, journal)
				return
			}
			if !waitScheduledControlRetry(ctx, r.journals, journal, logger, err) {
				return
			}
		}
	}
	if len(journal.PendingEvents) > 0 {
		// 此处上传失败已记录并保留事件，后续状态同步和终态提交会重试。
		_ = r.flushEvents(ctx, journal, logger)
	}

	processCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		r.runStateSyncLoop(processCtx, journal, commands, logger)
	}()
	var lastEventFlushAttempt time.Time
	report := func(eventType string, payload json.RawMessage) {
		journal.mu.Lock()
		journal.PendingEvents = append(journal.PendingEvents, workerprotocol.EventInput{
			Sequence: journal.NextSequence, Type: eventType, Payload: payload,
		})
		journal.NextSequence++
		if err := r.journals.save(journal); err != nil {
			logger.Error("持久化 Codex 事件失败", zap.Error(err))
			journal.mu.Unlock()
			return
		}
		now := time.Now()
		if shouldFlushRemoteEvents(lastEventFlushAttempt, now) {
			lastEventFlushAttempt = now
			_ = r.flushEventsLocked(processCtx, journal, logger)
		}
		journal.mu.Unlock()
	}
	result, err := r.processor.Process(processCtx, task, commands, report)
	cancel()
	<-heartbeatDone
	journal.mu.Lock()
	if err == nil {
		copyResult := result.Result
		journal.Result = &copyResult
	} else {
		journal.FailureCode = "worker_error"
		if errors.Is(err, errRemoteInterrupt) {
			journal.FailureCode = "user_interrupt"
		}
		var codexErr *workerprotocol.CodexTurnError
		if errors.As(err, &codexErr) && !codexErr.WillRetry {
			journal.FailureCode = "codex_non_retryable_error"
			journal.CodexError = codexErr
		}
		journal.Failure = err.Error()
	}
	if saveErr := r.journals.save(journal); saveErr != nil {
		logger.Error("持久化任务最终结果失败", zap.Error(saveErr))
		journal.mu.Unlock()
		return
	}
	journal.mu.Unlock()
	r.releaseRun(journal)
	releaseSlot()
	r.deliverTerminal(ctx, journal, logger)
}

func shouldFlushRemoteEvents(lastAttempt, now time.Time) bool {
	return lastAttempt.IsZero() || now.Sub(lastAttempt) >= remoteEventFlushInterval
}

func (r *runtimeExecutor) syncRunState(ctx context.Context, journal *runJournal,
	commands chan<- workerprotocol.RunCommand,
	logger *zap.Logger,
) error {
	journal.mu.Lock()
	task := journal.Task
	terminal := journal.Result != nil || journal.Failure != ""
	var desktopRequest *workerprotocol.DesktopTurnPrepareRequest
	if journal.DesktopRequest != nil {
		copyRequest := *journal.DesktopRequest
		desktopRequest = &copyRequest
	}
	decisions := append([]appliedInputDecision(nil), journal.AppliedInputs...)
	journal.mu.Unlock()
	if desktopRequest != nil {
		if err := r.restoreDesktopThread(ctx, *desktopRequest); err != nil {
			return err
		}
	}
	requestCtx, cancel := context.WithTimeout(ctx, r.cfg.ControlTimeout)
	var err error
	if desktopRequest != nil {
		_, err = r.client.PrepareDesktopTurn(requestCtx, *desktopRequest)
	} else {
		err = r.client.DecideInput(requestCtx, &task, "start", task.Claimed.ConfirmedTurnID)
	}
	cancel()
	if err != nil {
		logger.Warn("补报 Worker 本地 Run 失败，本地任务继续运行", zap.Error(err))
		return err
	}
	if desktopRequest != nil {
		if desktopRequest.TurnID == "" || (task.Claimed.ConfirmedTurnID != "" && task.Claimed.ConfirmedTurnID != desktopRequest.TurnID) {
			return errors.New("缺失或冲突的 Desktop Journal 原生 Turn ID，禁止推断确认结果")
		}
		requestCtx, cancel = context.WithTimeout(ctx, r.cfg.ControlTimeout)
		err = r.client.RecordSubmission(requestCtx, &task, desktopRequest.TurnID)
		cancel()
		if err != nil {
			return err
		}
		requestCtx, cancel = context.WithTimeout(ctx, r.cfg.ControlTimeout)
		err = r.client.ConfirmTurn(requestCtx, &task, desktopRequest.TurnID)
		cancel()
		if err != nil {
			return err
		}
	} else {
		// 原生执行可以先于 Control 登记完成；只重报 Journal 中实际观测到的身份，
		// 不从提交 ID 或最终结果推断确认 ID。任一阶段失败都保留 Journal 重试。
		for _, identity := range []struct {
			id     string
			report func(context.Context, *workerprotocol.Task, string) error
		}{
			{task.Claimed.ExternalThreadID, r.client.SetThread},
			{task.Claimed.SubmissionID, r.client.RecordSubmission},
			{task.Claimed.ConfirmedTurnID, r.client.ConfirmTurn},
		} {
			if identity.id == "" {
				continue
			}
			requestCtx, cancel = context.WithTimeout(ctx, r.cfg.ControlTimeout)
			err = identity.report(requestCtx, &task, identity.id)
			cancel()
			if err != nil {
				return err
			}
		}
	}
	for _, decision := range decisions {
		decisionTask := task
		decisionTask.Claimed.ID = decision.InputID
		requestCtx, cancel = context.WithTimeout(ctx, r.cfg.ControlTimeout)
		err = r.client.DecideInput(requestCtx, &decisionTask, decision.Action, decision.TurnID)
		cancel()
		if err != nil {
			logger.Warn("补报 Worker 本地输入决议失败", zap.Error(err))
			return err
		}
	}
	// 已完成的 Desktop Journal 只补报登记、事件和终态，不再对终态 Run 发送运行心跳。
	if desktopRequest != nil && terminal {
		return nil
	}
	requestCtx, cancel = context.WithTimeout(ctx, r.cfg.ControlTimeout)
	response, err := r.client.RunHeartbeat(requestCtx, &task)
	cancel()
	if err != nil {
		logger.Warn("同步 Worker 本地 Run 状态失败", zap.Error(err))
		return err
	}
	deliverCommands(commands, response.Commands)
	return nil
}

func (r *runtimeExecutor) runStateSyncLoop(ctx context.Context, journal *runJournal,
	commands chan<- workerprotocol.RunCommand,
	logger *zap.Logger,
) {
	if r.abandonIfPermanentDesktopSync(ctx, journal, commands, logger) {
		return
	}
	ticker := time.NewTicker(r.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if r.abandonIfPermanentDesktopSync(context.Background(), journal, commands, logger) {
				return
			}
		}
	}
}

func (r *runtimeExecutor) abandonIfPermanentDesktopSync(ctx context.Context, journal *runJournal,
	commands chan<- workerprotocol.RunCommand, logger *zap.Logger,
) bool {
	err := r.syncRunState(ctx, journal, commands, logger)
	if err == nil || journal.DesktopRequest == nil || retryableControlError(err) {
		return false
	}
	logger.Warn("Desktop Run 补登记被 Control 永久拒绝，停止补报", zap.Error(err))
	abandonRunJournal(r.journals, journal)
	return true
}

func deliverCommands(target chan<- workerprotocol.RunCommand,
	commands []workerprotocol.RunCommand,
) {
	for _, command := range commands {
		select {
		case target <- command:
		default:
			return
		}
	}
}

func (r *runtimeExecutor) flushEvents(ctx context.Context, journal *runJournal, logger *zap.Logger) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return r.flushEventsLocked(ctx, journal, logger)
}

func (r *runtimeExecutor) flushEventsLocked(ctx context.Context, journal *runJournal,
	logger *zap.Logger,
) error {
	if len(journal.PendingEvents) == 0 {
		return nil
	}
	requestCtx, cancel := context.WithTimeout(ctx, r.cfg.ControlTimeout)
	defer cancel()
	if err := r.client.Events(requestCtx, &journal.Task, journal.PendingEvents); err != nil {
		logger.Warn("上传 Codex 事件失败，已保留在 Journal", zap.Error(err))
		return err
	}
	journal.PendingEvents = nil
	if err := r.journals.save(journal); err != nil {
		logger.Error("确认事件上传状态失败", zap.Error(err))
		return err
	}
	return nil
}

func (r *runtimeExecutor) deliverTerminal(ctx context.Context, journal *runJournal,
	logger *zap.Logger,
) {
	for ctx.Err() == nil {
		if journal.ControlAbandoned {
			return
		}
		var syncErr error
		if !journal.TerminalDelivered {
			// Run 可能在 Control 全程离线期间已经结束；先幂等补登记，
			// 再提交事件和终态，避免未登记的终态永久 404。
			syncErr = r.syncRunState(ctx, journal, nil, logger)
			if syncErr != nil && !retryableControlError(syncErr) {
				logger.Warn("Run 补登记或身份确认被 Control 永久拒绝，保留 Journal 并停止补报", zap.Error(syncErr))
				abandonRunJournal(r.journals, journal)
				return
			}
			if syncErr != nil {
				if !waitScheduledControlRetry(ctx, r.journals, journal, logger, syncErr) {
					return
				}
				continue
			}
		}
		flushErr := r.flushEvents(ctx, journal, logger)
		var completeErr error
		if !journal.TerminalDelivered {
			requestCtx, cancel := context.WithTimeout(ctx, r.cfg.ControlTimeout)
			if journal.Result != nil {
				completeErr = r.client.Complete(requestCtx, &journal.Task, *journal.Result)
			} else {
				cause := errors.New(journal.Failure)
				completeErr = r.client.FailWithCodexError(requestCtx, &journal.Task,
					journal.FailureCode, cause, journal.CodexError)
			}
			cancel()
			if completeErr == nil || workerprotocol.IsAlreadyFinished(completeErr) {
				journal.TerminalDelivered = true
				journal.clearControlRetry()
				if saveErr := r.journals.save(journal); saveErr != nil {
					logger.Error("持久化最终结果提交状态失败", zap.Error(saveErr))
				}
			} else {
				logger.Warn("提交最终结果失败，稍后重试", zap.Error(completeErr))
				if !retryableControlError(completeErr) {
					if controlHTTPStatus(completeErr) == http.StatusNotFound &&
						journal.DesktopRequest != nil &&
						(syncErr == nil || retryableControlError(syncErr)) &&
						syncErr != nil {
						if !waitScheduledControlRetry(ctx, r.journals, journal, logger, syncErr) {
							return
						}
						continue
					}
					abandonRunJournal(r.journals, journal)
					return
				}
			}
		}
		if flushErr != nil && !retryableControlError(flushErr) && journal.TerminalDelivered {
			abandonRunJournal(r.journals, journal)
			return
		}
		if journal.TerminalDelivered && len(journal.PendingEvents) == 0 {
			if removeErr := r.journals.remove(journal.Task.Claimed.RunID); removeErr != nil {
				logger.Error("删除已确认的 Run Journal 失败", zap.Error(removeErr))
			}
			return
		}
		fail := completeErr
		if fail == nil {
			fail = flushErr
		}
		if fail == nil {
			fail = syncErr
		}
		if !waitScheduledControlRetry(ctx, r.journals, journal, logger, fail) {
			return
		}
	}
}
