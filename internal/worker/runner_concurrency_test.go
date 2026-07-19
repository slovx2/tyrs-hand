package worker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/devenv"
	"github.com/slovx2/tyrs-hand/internal/queue"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type concurrencyQueue struct {
	mu        sync.Mutex
	jobs      []*queue.ClaimedJob
	claimed   int
	completed int
}

func (q *concurrencyQueue) Claim(context.Context, string) (*queue.ClaimedJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.claimed == len(q.jobs) {
		return nil, nil
	}
	job := q.jobs[q.claimed]
	q.claimed++
	return job, nil
}

func (*concurrencyQueue) Heartbeat(context.Context, uuid.UUID, string, int64) error { return nil }

func (q *concurrencyQueue) Complete(context.Context, uuid.UUID, string, int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.completed++
	return nil
}

func (*concurrencyQueue) Block(context.Context, uuid.UUID, string, int64, error) error { return nil }
func (*concurrencyQueue) Fail(context.Context, uuid.UUID, string, int64, error) error  { return nil }
func (*concurrencyQueue) RequeueExpired(context.Context) (int64, error)                { return 0, nil }

type blockingProcessor struct {
	mu        sync.Mutex
	started   chan struct{}
	release   chan struct{}
	active    int
	maxActive int
}

func (p *blockingProcessor) Process(context.Context, *queue.ClaimedJob) error {
	p.mu.Lock()
	p.active++
	if p.active > p.maxActive {
		p.maxActive = p.active
	}
	p.mu.Unlock()
	p.started <- struct{}{}
	<-p.release
	p.mu.Lock()
	p.active--
	p.mu.Unlock()
	return nil
}

func TestRunnerLimitsConcurrentJobsToSix(t *testing.T) {
	jobs := make([]*queue.ClaimedJob, 7)
	for index := range jobs {
		jobs[index] = &queue.ClaimedJob{}
		jobs[index].ID = uuid.New()
		jobs[index].LeaseToken = "lease"
		jobs[index].LeaseEpoch = 1
	}
	jobQueue := &concurrencyQueue{jobs: jobs}
	processor := &blockingProcessor{started: make(chan struct{}, 7), release: make(chan struct{})}
	runner := &Runner{
		cfg:   config.Config{WorkerID: "worker", WorkerMaxConcurrentJobs: 6, HeartbeatInterval: time.Hour},
		queue: jobQueue, processor: processor, logger: zap.NewNop(),
	}
	slots := make(chan struct{}, 6)
	var active sync.WaitGroup
	runner.fillSlots(context.Background(), slots, &active)

	for range 6 {
		select {
		case <-processor.started:
		case <-time.After(time.Second):
			t.Fatal("前六个任务没有及时启动")
		}
	}
	select {
	case <-processor.started:
		t.Fatal("第七个任务不应越过并发槽位")
	case <-time.After(50 * time.Millisecond):
	}
	jobQueue.mu.Lock()
	require.Equal(t, 6, jobQueue.claimed)
	jobQueue.mu.Unlock()

	close(processor.release)
	active.Wait()
	runner.fillSlots(context.Background(), slots, &active)
	select {
	case <-processor.started:
	case <-time.After(time.Second):
		t.Fatal("槽位释放后第七个任务没有启动")
	}
	active.Wait()
	processor.mu.Lock()
	require.Equal(t, 6, processor.maxActive)
	processor.mu.Unlock()
	jobQueue.mu.Lock()
	require.Equal(t, 7, jobQueue.completed)
	jobQueue.mu.Unlock()
}

func TestDegradedEnvironmentAddsAgentDiagnosticContext(t *testing.T) {
	contextEntries := environmentAdditionalContext(devenv.Result{
		Status: "degraded",
		Diagnostics: []devenv.Diagnostic{{
			Stage: "dependencies", Project: "api", Manager: "uv", Message: "simulated failure",
		}},
	})
	entry, exists := contextEntries["tyrs_hand_development_environment"]
	require.True(t, exists)
	require.Equal(t, "application", entry.Kind)
	require.Contains(t, entry.Value, "simulated failure")
	require.Contains(t, entry.Value, "继续完成任务")
	require.Nil(t, environmentAdditionalContext(devenv.Result{Status: "ready"}))
}
