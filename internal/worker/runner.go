package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/queue"
	"go.uber.org/zap"
)

type Runner struct {
	cfg       config.Config
	db        *sql.DB
	redis     *redis.Client
	queue     *queue.Repository
	processor *Processor
	logger    *zap.Logger
}

func NewRunner(cfg config.Config, db *sql.DB, redisClient *redis.Client, queueRepository *queue.Repository, processor *Processor, logger *zap.Logger) *Runner {
	return &Runner{cfg: cfg, db: db, redis: redisClient, queue: queueRepository, processor: processor, logger: logger}
}

func (r *Runner) Run(ctx context.Context) error {
	if err := r.register(ctx); err != nil {
		return err
	}
	go r.workerHeartbeat(ctx)
	recoveryTicker := time.NewTicker(30 * time.Second)
	defer recoveryTicker.Stop()
	idle := time.NewTicker(2 * time.Second)
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-recoveryTicker.C:
			if count, err := r.queue.RequeueExpired(ctx); err != nil {
				r.logger.Error("恢复过期任务失败", zap.Error(err))
			} else if count > 0 {
				r.logger.Warn("已恢复过期任务", zap.Int64("count", count))
			}
		case <-idle.C:
			claimed, err := r.queue.Claim(ctx, r.cfg.WorkerID)
			if err != nil {
				r.logger.Error("领取任务失败", zap.Error(err))
				continue
			}
			if claimed == nil {
				continue
			}
			r.execute(ctx, claimed)
		}
	}
}

func (r *Runner) execute(parent context.Context, claimed *queue.ClaimedJob) {
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
				if err := r.queue.Heartbeat(ctx, claimed.ID, claimed.LeaseToken, claimed.LeaseEpoch); err != nil {
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
	err := r.processor.Process(ctx, claimed)
	cancel()
	<-heartbeatDone
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer finishCancel()
	if err == nil {
		if completeErr := r.queue.Complete(finishCtx, claimed.ID, claimed.LeaseToken, claimed.LeaseEpoch); completeErr != nil {
			r.logger.Error("提交任务完成状态失败", zap.Error(completeErr), zap.String("job_id", claimed.ID.String()))
		} else {
			r.publish(finishCtx, "job.succeeded", claimed.ID.String())
		}
	} else {
		r.logger.Error("任务执行失败", zap.String("job_id", claimed.ID.String()), zap.Error(err))
		if finishErr := r.queue.Fail(finishCtx, claimed.ID, claimed.LeaseToken, claimed.LeaseEpoch, err); finishErr != nil && !errors.Is(finishErr, queue.ErrLeaseLost) {
			r.logger.Error("记录任务失败状态失败", zap.Error(finishErr))
		}
		r.publish(finishCtx, "job.failed", claimed.ID.String())
	}
}

func (r *Runner) register(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO worker_nodes(id, version, status, metadata)
		VALUES ($1, '0.1.0', 'online', $2)
		ON CONFLICT(id) DO UPDATE SET version = EXCLUDED.version, status = 'online', heartbeat_at = now(), started_at = now()`,
		r.cfg.WorkerID, []byte(`{"agent":"codex"}`))
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
	data, _ := json.Marshal(map[string]string{"type": eventType, "id": id})
	_ = r.redis.Publish(ctx, "tyrs-hand:events", data).Err()
}
