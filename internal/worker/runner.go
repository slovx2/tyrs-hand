package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/config"
	"go.uber.org/zap"
)

type Runner struct {
	cfg       config.Config
	db        *sql.DB
	redis     *redis.Client
	controls  controlQueue
	processor jobProcessor
	logger    *zap.Logger
}

type controlQueue interface {
	Claim(context.Context, string) (*codexcontrol.ClaimedControl, error)
	Heartbeat(context.Context, *codexcontrol.ClaimedControl) error
	Complete(context.Context, *codexcontrol.ClaimedControl, codexcontrol.TurnResult) error
	Cancel(context.Context, *codexcontrol.ClaimedControl, string, string) error
	Fail(context.Context, *codexcontrol.ClaimedControl, string, error) error
	Reconcile(context.Context, *codexcontrol.ClaimedControl, string, error) error
	ReplySatisfied(context.Context, *codexcontrol.ClaimedControl) (bool, error)
	RequeueExpired(context.Context) (int64, error)
}

type jobProcessor interface {
	Process(context.Context, *codexcontrol.ClaimedControl) (codexcontrol.TurnResult, error)
}

func NewRunner(cfg config.Config, db *sql.DB, redisClient *redis.Client, controls *codexcontrol.Repository, processor *Processor, logger *zap.Logger) *Runner {
	return &Runner{cfg: cfg, db: db, redis: redisClient, controls: controls, processor: processor, logger: logger}
}

func (r *Runner) Run(ctx context.Context) error {
	if err := r.register(ctx); err != nil {
		return err
	}
	go r.workerHeartbeat(ctx)
	var wakeups <-chan *redis.Message
	var subscription *redis.PubSub
	if r.redis != nil {
		subscription = r.redis.Subscribe(ctx, codexcontrol.WakeupChannel)
		defer func() { _ = subscription.Close() }()
		wakeups = subscription.Channel()
	}
	slots := make(chan struct{}, r.cfg.WorkerMaxConcurrentJobs)
	var active sync.WaitGroup
	recoveryTicker := time.NewTicker(30 * time.Second)
	defer recoveryTicker.Stop()
	idle := time.NewTicker(2 * time.Second)
	defer idle.Stop()
	r.fillSlots(ctx, slots, &active)
	for {
		select {
		case <-ctx.Done():
			done := make(chan struct{})
			go func() {
				active.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				r.logger.Warn("Worker 优雅退出超时，未完成任务将等待 Lease 过期后重新入队")
			}
			return ctx.Err()
		case <-recoveryTicker.C:
			if count, err := r.controls.RequeueExpired(ctx); err != nil {
				r.logger.Error("恢复过期任务失败", zap.Error(err))
			} else if count > 0 {
				r.logger.Warn("已恢复过期任务", zap.Int64("count", count))
			}
		case <-idle.C:
			r.fillSlots(ctx, slots, &active)
		case _, open := <-wakeups:
			if !open {
				wakeups = nil
				continue
			}
			r.fillSlots(ctx, slots, &active)
		}
	}
}

func (r *Runner) fillSlots(ctx context.Context, slots chan struct{}, active *sync.WaitGroup) {
	for {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		default:
			return
		}
		claimed, err := r.controls.Claim(ctx, r.cfg.WorkerID)
		if err != nil {
			<-slots
			r.logger.Error("领取任务失败", zap.Error(err))
			return
		}
		if claimed == nil {
			<-slots
			return
		}
		active.Add(1)
		go func() {
			defer active.Done()
			defer func() { <-slots }()
			r.execute(ctx, claimed)
		}()
	}
}

func (r *Runner) execute(parent context.Context, claimed *codexcontrol.ClaimedControl) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(r.cfg.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := r.controls.Heartbeat(ctx, claimed); err != nil {
					r.logger.Error("任务心跳失败", zap.Error(err), zap.String("job_id", claimed.ID.String()))
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	r.publish(ctx, "job.started", claimed.ID.String())
	result, err := r.processor.Process(ctx, claimed)
	cancel()
	<-heartbeatDone
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer finishCancel()
	if err == nil {
		replied, replyErr := r.controls.ReplySatisfied(finishCtx, claimed)
		if replyErr != nil {
			err = replyErr
		} else if !replied {
			err = errors.New("required_reply_missing")
		}
	}
	if err == nil {
		if completeErr := r.controls.Complete(finishCtx, claimed, result); completeErr != nil {
			r.logger.Error("提交任务完成状态失败", zap.Error(completeErr), zap.String("job_id", claimed.ID.String()))
		} else {
			r.publish(finishCtx, "intent.completed", claimed.ID.String())
		}
	} else if discordStopRequested(finishCtx, r.db, claimed.ID, err) {
		_ = r.controls.Cancel(finishCtx, claimed, "user_interrupt", "stopped from Discord")
		r.logger.Info("Discord 任务已由用户停止", zap.String("job_id", claimed.ID.String()))
		r.publish(finishCtx, "intent.canceled", claimed.ID.String())
	} else if err.Error() == "required_reply_missing" {
		if claimed.SourceType == codexcontrol.SourceGitHub {
			if processor, ok := r.processor.(*Processor); ok {
				_ = processor.control.ReportFailure(finishCtx, claimed.Capability, "required_reply_missing")
			}
		}
		_ = r.controls.Fail(finishCtx, claimed, "required_reply_missing", err)
		r.publish(finishCtx, "intent.failed", claimed.ID.String())
	} else {
		r.logger.Error("任务执行失败", zap.String("job_id", claimed.ID.String()), zap.Error(err))
		if claimed.SourceType == codexcontrol.SourceGitHub && claimed.Attempt >= claimed.MaxAttempts {
			if processor, ok := r.processor.(*Processor); ok {
				_ = processor.control.ReportFailure(finishCtx, claimed.Capability, "control_error")
			}
		}
		if finishErr := r.controls.Reconcile(finishCtx, claimed, "control_error", err); finishErr != nil && !errors.Is(finishErr, codexcontrol.ErrLeaseLost) {
			r.logger.Error("记录任务失败状态失败", zap.Error(finishErr))
		}
		r.publish(finishCtx, "intent.reconciling", claimed.ID.String())
	}
}

func (r *Runner) register(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO worker_nodes(id, version, status, metadata)
		VALUES ($1, '0.1.0', 'online', $2)
		ON CONFLICT(id) DO UPDATE SET version = EXCLUDED.version, status = 'online', heartbeat_at = now(), started_at = now()`,
		r.cfg.WorkerID, []byte(fmt.Sprintf(`{"agent":"codex","maxConcurrentJobs":%d}`, r.cfg.WorkerMaxConcurrentJobs)))
	return err
}

func (r *Runner) workerHeartbeat(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_, _ = r.db.ExecContext(ctx, `UPDATE worker_nodes SET heartbeat_at = now(), status = 'online' WHERE id = $1`, r.cfg.WorkerID)
		case <-ctx.Done():
			_, _ = r.db.ExecContext(context.Background(), `UPDATE worker_nodes SET status = 'offline' WHERE id = $1`, r.cfg.WorkerID)
			return
		}
	}
}

func (r *Runner) publish(ctx context.Context, eventType, id string) {
	if r.redis == nil {
		return
	}
	data, _ := json.Marshal(map[string]string{"type": eventType, "id": id})
	_ = r.redis.Publish(ctx, "tyrs-hand:events", data).Err()
}
