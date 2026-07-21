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

func (r *RemoteRunner) runJournal(ctx context.Context, journal *runJournal, slots chan struct{},
	active *sync.WaitGroup,
) {
	defer active.Done()
	defer func() { <-slots }()
	task := &journal.Task
	logger := r.logger.With(zap.String("run_id", task.Claimed.RunID.String()),
		zap.String("intent_id", task.Claimed.ID.String()))

	commands := make(chan workerprotocol.RunCommand, 16)
	if !r.restoreLease(ctx, task, commands, logger) {
		return
	}
	if err := r.journals.save(journal); err != nil {
		logger.Error("持久化恢复后的 Run 状态失败", zap.Error(err))
		return
	}
	if len(journal.PendingEvents) > 0 {
		r.flushEvents(ctx, journal, logger)
	}
	if journal.Result != nil || journal.Failure != "" {
		r.deliverTerminal(ctx, journal, logger)
		return
	}

	processCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		r.runLeaseHeartbeat(processCtx, task, commands, logger)
	}()
	var journalMu sync.Mutex
	var lastEventFlushAttempt time.Time
	report := func(eventType string, payload json.RawMessage) {
		journalMu.Lock()
		journal.PendingEvents = append(journal.PendingEvents, workerprotocol.EventInput{
			Sequence: journal.NextSequence, Type: eventType, Payload: payload,
		})
		journal.NextSequence++
		if err := r.journals.save(journal); err != nil {
			logger.Error("持久化 Codex 事件失败", zap.Error(err))
			journalMu.Unlock()
			return
		}
		now := time.Now()
		if shouldFlushRemoteEvents(lastEventFlushAttempt, now) {
			lastEventFlushAttempt = now
			r.flushEventsLocked(processCtx, journal, logger)
		}
		journalMu.Unlock()
	}
	result, err := r.processor.ProcessRemote(processCtx, task, commands, report)
	cancel()
	<-heartbeatDone
	journalMu.Lock()
	if err == nil {
		copyResult := result.Result
		journal.Result = &copyResult
	} else {
		journal.FailureCode = "worker_error"
		if errors.Is(err, errRemoteInterrupt) {
			journal.FailureCode = "user_interrupt"
		}
		journal.Failure = err.Error()
	}
	if saveErr := r.journals.save(journal); saveErr != nil {
		logger.Error("持久化任务最终结果失败", zap.Error(saveErr))
		journalMu.Unlock()
		return
	}
	journalMu.Unlock()
	r.deliverTerminal(ctx, journal, logger)
}

func shouldFlushRemoteEvents(lastAttempt, now time.Time) bool {
	return lastAttempt.IsZero() || now.Sub(lastAttempt) >= remoteEventFlushInterval
}

func (r *RemoteRunner) restoreLease(ctx context.Context, task *workerprotocol.Task,
	commands chan<- workerprotocol.RunCommand,
	logger *zap.Logger,
) bool {
	for ctx.Err() == nil {
		requestCtx, cancel := context.WithTimeout(ctx, r.cfg.ControlTimeout)
		response, err := r.client.RunHeartbeat(requestCtx, task)
		cancel()
		if err == nil {
			applyRunRecovery(task, response.Recovery)
			deliverCommands(commands, response.Commands)
			return true
		}
		if workerprotocol.IsLeaseLost(err) {
			logger.Error("Run Lease 已失效，需要管理员在 Control 侧对账", zap.Error(err))
			return false
		}
		logger.Warn("恢复 Run Lease 失败，等待 Control 恢复", zap.Error(err))
		if !waitContext(ctx, 3*time.Second) {
			return false
		}
	}
	return false
}

func applyRunRecovery(task *workerprotocol.Task, recovery workerprotocol.RunRecoveryState) {
	if task == nil {
		return
	}
	task.Claimed.Recovering = recovery.Recovering
	task.Claimed.SubmissionID = recovery.SubmissionID
	task.Claimed.ConfirmedTurnID = recovery.ConfirmedTurnID
	if recovery.ExternalThreadID != "" {
		task.Claimed.ExternalThreadID = recovery.ExternalThreadID
	}
	if recovery.CodexHomeKey != "" {
		task.Claimed.CodexHomeKey = recovery.CodexHomeKey
	}
	if recovery.ProviderSignature != "" {
		task.Claimed.ProviderSignature = recovery.ProviderSignature
	}
}

func (r *RemoteRunner) runLeaseHeartbeat(ctx context.Context, task *workerprotocol.Task,
	commands chan<- workerprotocol.RunCommand,
	logger *zap.Logger,
) {
	ticker := time.NewTicker(r.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			requestCtx, cancel := context.WithTimeout(context.Background(), r.cfg.ControlTimeout)
			response, err := r.client.RunHeartbeat(requestCtx, task)
			cancel()
			if err != nil {
				// 网络中断不能终止本地 Codex；最终结果由 Journal 负责补交。
				logger.Warn("Run 续租失败，本地任务继续运行", zap.Error(err))
			} else {
				deliverCommands(commands, response.Commands)
			}
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

func (r *RemoteRunner) flushEvents(ctx context.Context, journal *runJournal, logger *zap.Logger) {
	if len(journal.PendingEvents) == 0 {
		return
	}
	requestCtx, cancel := context.WithTimeout(ctx, r.cfg.ControlTimeout)
	defer cancel()
	if err := r.client.Events(requestCtx, &journal.Task, journal.PendingEvents); err != nil {
		logger.Warn("上传 Codex 事件失败，已保留在 Journal", zap.Error(err))
		return
	}
	journal.PendingEvents = nil
	if err := r.journals.save(journal); err != nil {
		logger.Error("确认事件上传状态失败", zap.Error(err))
	}
}

func (r *RemoteRunner) flushEventsLocked(ctx context.Context, journal *runJournal,
	logger *zap.Logger,
) {
	r.flushEvents(ctx, journal, logger)
}

func (r *RemoteRunner) deliverTerminal(ctx context.Context, journal *runJournal,
	logger *zap.Logger,
) {
	for ctx.Err() == nil {
		r.flushEvents(ctx, journal, logger)
		requestCtx, cancel := context.WithTimeout(ctx, r.cfg.ControlTimeout)
		var err error
		if journal.Result != nil {
			err = r.client.Complete(requestCtx, &journal.Task, *journal.Result)
		} else {
			cause := errors.New(journal.Failure)
			err = r.client.Fail(requestCtx, &journal.Task, journal.FailureCode, cause)
		}
		cancel()
		if err == nil || workerprotocol.IsAlreadyFinished(err) {
			if removeErr := r.journals.remove(journal.Task.Claimed.RunID); removeErr != nil {
				logger.Error("删除已确认的 Run Journal 失败", zap.Error(removeErr))
			}
			return
		}
		if workerprotocol.IsLeaseLost(err) {
			logger.Error("最终结果未被接受，Run Lease 已失效", zap.Error(err))
			return
		}
		logger.Warn("提交最终结果失败，稍后重试", zap.Error(err))
		if !waitContext(ctx, 3*time.Second) {
			return
		}
	}
}
