package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

// runtimeExecutor 只负责一个引擎的执行与补报。所有引擎由 Runner 的唯一领取循环派发。
type runtimeExecutor struct {
	engine      runtimeidentity.Engine
	cfg         config.Config
	client      *workerprotocol.Client
	processor   taskProcessor
	logger      *zap.Logger
	journals    *journalStore
	coordinator *runCoordinator
	wake        *wakeSignals
	claimWake   *wakeSignals
}

// AddRuntimeProcessor 必须在 Run 和 Control 唤醒通道启动之前调用。
func (r *Runner) AddRuntimeProcessor(p *Processor) error {
	if p == nil || p.client == nil || !p.cfg.ControlSyncEnabled() ||
		p.journals == nil || p.coordinator == nil || p.wake == nil {
		return errors.New("运行时缺少 Control 客户端或持久化状态")
	}
	if p.runtimeIdentity.WorkerID != r.WorkerID() || p.turnSlots != r.turnSlots {
		return errors.New("运行时必须属于同一 Worker 并共享并发预算")
	}
	return r.addExecutor(&runtimeExecutor{engine: p.runtimeIdentity.Engine, cfg: p.cfg,
		client: p.client, processor: p, logger: p.logger, journals: p.journals,
		coordinator: p.coordinator, wake: p.wake, claimWake: r.wake})
}

func (r *Runner) addExecutor(executor *runtimeExecutor) error {
	if err := executor.engine.Validate(); err != nil {
		return err
	}
	if executor.client == nil || executor.client.Engine() != executor.engine {
		return errors.New("运行时客户端引擎不匹配")
	}
	for _, existing := range r.executors {
		if existing.engine == executor.engine {
			return fmt.Errorf("运行时 %s 已注册", executor.engine)
		}
		if existing.cfg.WorkerDataRoot == executor.cfg.WorkerDataRoot ||
			existing.journals == executor.journals || existing.coordinator == executor.coordinator {
			return errors.New("运行时不能共享 Journal 或会话协调器")
		}
	}
	r.executors = append(r.executors, executor)
	return nil
}

// 每轮按序检查全部引擎，领取成功后从下一引擎开始，避免持续繁忙的队列饿死另一队列。
func (r *Runner) claimNext(ctx context.Context) (*runtimeExecutor, *workerprotocol.Task) {
	for offset := 0; offset < len(r.executors); offset++ {
		index := (r.nextExecutor + offset) % len(r.executors)
		executor := r.executors[index]
		claim, err := executor.client.Claim(ctx, workerprotocol.ClaimRequest{Role: r.claimRole()})
		if err != nil {
			r.logger.Warn("从 Control 领取运行时任务失败", zap.String("engine", string(executor.engine)), zap.Error(err))
			continue
		}
		if claim.Task == nil {
			continue
		}
		if claim.Task.Snapshot.Runtime.Engine != executor.engine || !r.roleAllowed(claim.Task.Claimed.SourceType) {
			r.logger.Error("拒绝 Control 返回的错误引擎或角色任务", zap.String("engine", string(executor.engine)))
			continue
		}
		r.nextExecutor = (index + 1) % len(r.executors)
		return executor, claim.Task
	}
	return nil, nil
}

func (r *Runner) recoverJournals(ctx context.Context, active *sync.WaitGroup) error {
	// 先检查所有目录，发现错误时不执行任何可能产生副作用的恢复。
	type pending struct {
		executor *runtimeExecutor
		journal  *runJournal
	}
	var pendingRuns []pending
	for _, executor := range r.executors {
		stored, err := executor.journals.loadAll()
		if err != nil {
			return err
		}
		for _, journal := range stored {
			if journal.Task.Snapshot.Runtime.Engine != executor.engine || !r.roleAllowed(journal.Task.Claimed.SourceType) {
				return fmt.Errorf("run Journal %s 与目录引擎或 Worker 角色不匹配", journal.Task.Claimed.RunID)
			}
			pendingRuns = append(pendingRuns, pending{executor, journal})
		}
	}
	for _, run := range pendingRuns {
		select {
		case r.turnSlots <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		commands := make(chan workerprotocol.RunCommand, 16)
		run.executor.coordinator.register(run.journal, commands)
		active.Add(1)
		go run.executor.runJournal(ctx, run.journal, commands, r.turnSlots, active)
	}
	return nil
}
