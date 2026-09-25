package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

// runtimeExecutor 只负责一个引擎的执行与补报。所有引擎由 Runner 的唯一领取循环派发。
type runtimeExecutor struct {
	engine         runtimeidentity.Engine
	cfg            config.Config
	client         *workerprotocol.Client
	processor      taskProcessor
	logger         *zap.Logger
	journals       *journalStore
	coordinator    *runCoordinator
	wake           *wakeSignals
	claimWake      *wakeSignals
	inputMu        sync.Mutex
	acceptedInputs map[uuid.UUID]bool
}

// 当前进程内保留已接受输入的墓碑，覆盖完成补报与较早发出的领取响应交错的窗口。
func (r *runtimeExecutor) remembersInput(id uuid.UUID) bool {
	r.inputMu.Lock()
	defer r.inputMu.Unlock()
	return r.acceptedInputs[id]
}

func (r *runtimeExecutor) rememberJournalInputs(journal *runJournal) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	r.inputMu.Lock()
	defer r.inputMu.Unlock()
	if r.acceptedInputs == nil {
		r.acceptedInputs = make(map[uuid.UUID]bool)
	}
	r.acceptedInputs[journal.Task.Claimed.ID] = true
	for _, decision := range journal.AppliedInputs {
		r.acceptedInputs[decision.InputID] = true
	}
}

func (r *runtimeExecutor) releaseRun(journal *runJournal) {
	r.rememberJournalInputs(journal)
	r.coordinator.unregister(journal.Task.Claimed.RunID)
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
		claim, err := executor.client.Claim(ctx, workerprotocol.ClaimRequest{Role: r.claimRole(),
			OnlyActive: len(r.turnSlots) >= cap(r.turnSlots), ActiveControlIDs: executor.coordinator.activeControlIDs()})
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
	ready := pendingRuns[:0]
	for _, run := range pendingRuns {
		run.executor.rememberJournalInputs(run.journal)
		if run.journal.ControlAbandoned {
			r.logger.Warn("保留未确认 Journal，等待对账，不重新执行",
				zap.String("engine", string(run.executor.engine)),
				zap.String("run_id", run.journal.Task.Claimed.RunID.String()))
			continue
		}
		ready = append(ready, run)
	}
	if len(ready) == 0 {
		return nil
	}
	// 登记所有已接受输入后再异步排队，不能等待并发槽而阻塞心跳和活动命令领取。
	// 调度器自身计入 WaitGroup，保证关闭时不会在 Wait 返回后启动恢复任务。
	active.Add(1)
	go func() {
		defer active.Done()
		for _, run := range ready {
			select {
			case r.turnSlots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			if ctx.Err() != nil {
				<-r.turnSlots
				return
			}
			commands := make(chan workerprotocol.RunCommand, 16)
			run.executor.coordinator.register(run.journal, commands)
			active.Add(1)
			go run.executor.runJournal(ctx, run.journal, commands, r.turnSlots, active)
		}
	}()
	return nil
}
