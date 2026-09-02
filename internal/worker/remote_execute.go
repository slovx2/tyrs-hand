package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

const remoteEventFlushInterval = time.Second

func (r *Runner) runJournal(ctx context.Context, journal *runJournal,
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
	defer releaseSlot()
	task := &journal.Task
	logger := r.logger.With(zap.String("run_id", task.Claimed.RunID.String()),
		zap.String("intent_id", task.Claimed.ID.String()))
	if journal.TerminalDelivered {
		releaseSlot()
		r.deliverTerminal(ctx, journal, logger)
		return
	}

	defer r.coordinator.unregister(task.Claimed.RunID)
	if task.Claimed.SubmissionID != "" || task.Claimed.ConfirmedTurnID != "" {
		task.Claimed.Recovering = true
	}
	if err := r.journals.save(journal); err != nil {
		logger.Error("持久化恢复后的 Run 状态失败", zap.Error(err))
		return
	}
	if len(journal.PendingEvents) > 0 {
		r.flushEvents(ctx, journal, logger)
	}
	if journal.Result != nil || journal.Failure != "" {
		r.coordinator.unregister(task.Claimed.RunID)
		releaseSlot()
		r.deliverTerminal(ctx, journal, logger)
		return
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
			r.flushEventsLocked(processCtx, journal, logger)
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
	r.coordinator.unregister(task.Claimed.RunID)
	releaseSlot()
	r.deliverTerminal(ctx, journal, logger)
}

func shouldFlushRemoteEvents(lastAttempt, now time.Time) bool {
	return lastAttempt.IsZero() || now.Sub(lastAttempt) >= remoteEventFlushInterval
}

func (r *Runner) syncRunState(ctx context.Context, journal *runJournal,
	commands chan<- workerprotocol.RunCommand,
	logger *zap.Logger,
) error {
	journal.mu.Lock()
	task := journal.Task
	var desktopRequest *workerprotocol.DesktopTurnPrepareRequest
	if journal.DesktopRequest != nil {
		copyRequest := *journal.DesktopRequest
		desktopRequest = &copyRequest
	}
	decisions := append([]appliedInputDecision(nil), journal.AppliedInputs...)
	journal.mu.Unlock()
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

func (r *Runner) runStateSyncLoop(ctx context.Context, journal *runJournal,
	commands chan<- workerprotocol.RunCommand,
	logger *zap.Logger,
) {
	_ = r.syncRunState(ctx, journal, commands, logger)
	ticker := time.NewTicker(r.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = r.syncRunState(context.Background(), journal, commands, logger)
		}
	}
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

func (r *Runner) flushEvents(ctx context.Context, journal *runJournal, logger *zap.Logger) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return r.flushEventsLocked(ctx, journal, logger)
}

func (r *Runner) flushEventsLocked(ctx context.Context, journal *runJournal,
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

func (r *Runner) deliverTerminal(ctx context.Context, journal *runJournal,
	logger *zap.Logger,
) {
	for ctx.Err() == nil {
		if !journal.TerminalDelivered {
			// Run 可能在 Control 全程离线期间已经结束；先幂等补登记，
			// 再提交事件和终态，避免未登记的终态永久 404。
			_ = r.syncRunState(ctx, journal, nil, logger)
		}
		r.flushEvents(ctx, journal, logger)
		if !journal.TerminalDelivered {
			requestCtx, cancel := context.WithTimeout(ctx, r.cfg.ControlTimeout)
			var err error
			if journal.Result != nil {
				err = r.client.Complete(requestCtx, &journal.Task, *journal.Result)
			} else {
				cause := errors.New(journal.Failure)
				err = r.client.FailWithCodexError(requestCtx, &journal.Task,
					journal.FailureCode, cause, journal.CodexError)
			}
			cancel()
			if err == nil || workerprotocol.IsAlreadyFinished(err) {
				journal.TerminalDelivered = true
				if saveErr := r.journals.save(journal); saveErr != nil {
					logger.Error("持久化最终结果提交状态失败", zap.Error(saveErr))
				}
			} else {
				logger.Warn("提交最终结果失败，稍后重试", zap.Error(err))
			}
		}
		if journal.TerminalDelivered && len(journal.PendingEvents) == 0 {
			if removeErr := r.journals.remove(journal.Task.Claimed.RunID); removeErr != nil {
				logger.Error("删除已确认的 Run Journal 失败", zap.Error(removeErr))
			}
			return
		}
		if !waitContext(ctx, 3*time.Second) {
			return
		}
	}
}
