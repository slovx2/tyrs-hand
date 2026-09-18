package httpapi

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

const workerWakeCoalesceWindow = 250 * time.Millisecond

// workerWakeSweepInterval 是 Control 侧的兜底扫描间隔。
// 定时任务按时间到期、没有对应的写入事件，需要由 Control 主动补发唤醒。
const workerWakeSweepInterval = 30 * time.Second

// workerWakeSignal 是发布到 Redis 唤醒频道的事件。
// WorkerID 为空时由分发器按数据库待办解析目标 Worker。
type workerWakeSignal struct {
	Kind     string `json:"kind"`
	WorkerID string `json:"workerId,omitempty"`
}

// startWorkerWakeDispatcher 订阅 Redis 唤醒频道，把待办解析成目标 Worker，
// 再通过控制通道推送唤醒通知。未协商唤醒能力的 Worker 会继续使用兜底扫描。
func (s *Server) startWorkerWakeDispatcher(ctx context.Context) {
	if s.redis == nil {
		return
	}
	go func() {
		pending := make(map[uuid.UUID]map[string]bool)
		subscription := s.redis.Subscribe(ctx, codexcontrol.WakeupChannel)
		defer func() { _ = subscription.Close() }()
		channel := subscription.Channel()
		ticker := time.NewTicker(workerWakeCoalesceWindow)
		defer ticker.Stop()
		sweep := time.NewTicker(workerWakeSweepInterval)
		defer sweep.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.flushWorkerWakes(ctx, pending)
			case <-sweep.C:
				s.collectWorkerWake(ctx, pending, "reconcile")
				s.flushWorkerWakes(ctx, pending)
			case message, open := <-channel:
				if !open {
					return
				}
				s.collectWorkerWake(ctx, pending, message.Payload)
			}
		}
	}()
}

// publishWorkerWake 发布唤醒信号。workerID 为空时由分发器解析目标 Worker。
func (s *Server) publishWorkerWake(ctx context.Context, kind string, workerID uuid.UUID) {
	if s.redis == nil || kind == "" {
		return
	}
	signal := workerWakeSignal{Kind: kind}
	if workerID != uuid.Nil {
		signal.WorkerID = workerID.String()
	}
	payload, err := json.Marshal(signal)
	if err != nil {
		return
	}
	_ = s.redis.Publish(ctx, codexcontrol.WakeupChannel, payload).Err()
}

// collectWorkerWake 解析一条唤醒信号并累积到待推送集合。
// 旧的 "queued"/"reconcile" 负载按通用唤醒处理，一次解析全部待办类型。
func (s *Server) collectWorkerWake(ctx context.Context,
	pending map[uuid.UUID]map[string]bool, payload string,
) {
	kinds := []string(nil)
	workerID := uuid.Nil
	var signal workerWakeSignal
	if err := json.Unmarshal([]byte(payload), &signal); err == nil && signal.Kind != "" {
		if parsed, err := uuid.Parse(signal.WorkerID); err == nil {
			workerID = parsed
		}
		kinds = wakeKinds(signal.Kind)
	} else {
		kinds = wakeKinds(strings.TrimSpace(payload))
	}
	if len(kinds) == 0 {
		return
	}
	if workerID != uuid.Nil {
		for _, kind := range kinds {
			addWorkerWake(pending, workerID, kind)
		}
		return
	}
	for _, kind := range kinds {
		targets, err := s.resolveWorkerWakeTargets(ctx, kind)
		if err != nil {
			if ctx.Err() == nil && s.logger != nil {
				s.logger.Warn("解析 Worker 唤醒目标失败",
					zap.String("kind", kind), zap.Error(err))
			}
			continue
		}
		for _, id := range targets {
			addWorkerWake(pending, id, kind)
		}
	}
}

func wakeKinds(kind string) []string {
	switch kind {
	case workerprotocol.WakeClaim, workerprotocol.WakeSessionTitle,
		workerprotocol.WakeThreadSync, workerprotocol.WakeWorkspace,
		workerprotocol.WakeSSHConfig:
		return []string{kind}
	case "queued", "reconcile", "":
		// 旧发布点只带通用负载，按可能受影响的三类待办解析。
		return []string{workerprotocol.WakeClaim, workerprotocol.WakeSessionTitle,
			workerprotocol.WakeThreadSync}
	default:
		return nil
	}
}

func addWorkerWake(pending map[uuid.UUID]map[string]bool, workerID uuid.UUID, kind string) {
	if workerID == uuid.Nil || kind == "" {
		return
	}
	kinds := pending[workerID]
	if kinds == nil {
		kinds = make(map[string]bool)
		pending[workerID] = kinds
	}
	kinds[kind] = true
}

func (s *Server) flushWorkerWakes(ctx context.Context,
	pending map[uuid.UUID]map[string]bool,
) {
	if len(pending) == 0 {
		return
	}
	for workerID, kinds := range pending {
		list := make([]string, 0, len(kinds))
		for kind := range kinds {
			list = append(list, kind)
		}
		sort.Strings(list)
		if !s.notifyWorkerKinds(workerID, list) && ctx.Err() == nil && s.logger != nil {
			s.logger.Debug("Worker 唤醒未送达，等待兜底扫描",
				zap.String("worker_id", workerID.String()))
		}
		delete(pending, workerID)
	}
}

// resolveWorkerWakeTargets 查询具有指定待办的 Worker。
// workspace 与 ssh_config 无法按行定位时返回所有已连接的 Worker。
func (s *Server) resolveWorkerWakeTargets(ctx context.Context,
	kind string,
) ([]uuid.UUID, error) {
	switch kind {
	case workerprotocol.WakeClaim:
		// 待领取的 Turn 与已到期的定时任务都会让 Worker 发起一次领取。
		return s.queryWorkerWakeTargets(ctx, `SELECT DISTINCT control.worker_id
			FROM codex_turn_intents intent
			JOIN codex_thread_controls control ON control.id = intent.control_id
			WHERE control.worker_id IS NOT NULL
				AND control.lifecycle_state = 'active'
				AND intent.status IN ('queued','retry_wait','reconciling')
				AND intent.available_at <= now()
				AND intent.resolved_action IS NULL
			UNION
			SELECT DISTINCT workspace.worker_id
			FROM scheduled_tasks task
			JOIN worker_workspaces workspace ON workspace.id = task.workspace_id
			WHERE workspace.worker_id IS NOT NULL
				AND task.status = 'active'
				AND task.next_run_at IS NOT NULL
				AND task.next_run_at <= now()
				AND (task.blocked_until IS NULL OR task.blocked_until <= now())`)
	case workerprotocol.WakeSessionTitle:
		return s.queryWorkerWakeTargets(ctx, `SELECT DISTINCT workspace.worker_id
			FROM workspace_session_title_tasks task
			JOIN worker_workspaces workspace ON workspace.id = task.workspace_id
			WHERE workspace.worker_id IS NOT NULL
				AND task.attempt_count < 3
				AND task.next_attempt_at <= now()
				AND (task.status = 'pending'
					OR (task.status = 'claimed' AND task.lease_expires_at < now()))`)
	case workerprotocol.WakeThreadSync:
		return s.queryWorkerWakeTargets(ctx, `SELECT DISTINCT workspace.worker_id
			FROM codex_thread_controls control
			JOIN worker_workspaces workspace ON workspace.id = control.workspace_id
			WHERE workspace.worker_id IS NOT NULL
				AND ((control.desired_thread_name_source = 'fallback'
						AND control.desired_thread_name_revision >
							control.applied_thread_name_revision
						AND control.external_thread_id IS NOT NULL)
					OR EXISTS (SELECT 1 FROM codex_thread_lifecycle_requests request
						WHERE request.control_id = control.id
							AND request.source IN ('discord','client')
							AND request.status IN ('waiting_for_turn','applying')
							AND request.response IS NULL))`)
	case workerprotocol.WakeWorkspace, workerprotocol.WakeSSHConfig:
		return s.connectedWorkerIDs(), nil
	default:
		return nil, nil
	}
}

func (s *Server) queryWorkerWakeTargets(ctx context.Context, query string) ([]uuid.UUID, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func (s *Server) connectedWorkerIDs() []uuid.UUID {
	s.workerRPCMu.RLock()
	defer s.workerRPCMu.RUnlock()
	result := make([]uuid.UUID, 0, len(s.workerRPCConns))
	for id := range s.workerRPCConns {
		result = append(result, id)
	}
	return result
}
