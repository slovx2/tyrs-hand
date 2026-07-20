package worker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type concurrencyQueue struct {
	mu        sync.Mutex
	jobs      []*codexcontrol.ClaimedControl
	claimed   int
	completed int
	sources   []string
}

func (q *concurrencyQueue) ClaimSource(_ context.Context, _ string, source string) (*codexcontrol.ClaimedControl, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sources = append(q.sources, source)
	if q.claimed == len(q.jobs) {
		return nil, nil
	}
	job := q.jobs[q.claimed]
	q.claimed++
	return job, nil
}

func TestRunnerFiltersClaimsByWorkerRole(t *testing.T) {
	for _, test := range []struct{ role, source string }{
		{role: "github", source: codexcontrol.SourceGitHub},
		{role: "discord", source: codexcontrol.SourceDiscord},
		{role: "all", source: ""},
	} {
		t.Run(test.role, func(t *testing.T) {
			queue := &concurrencyQueue{}
			runner := &Runner{cfg: config.Config{WorkerID: "worker", WorkerRole: test.role,
				WorkerMaxConcurrentJobs: 1}, controls: queue, logger: zap.NewNop()}
			runner.fillSlots(context.Background(), make(chan struct{}, 1), &sync.WaitGroup{})
			require.Equal(t, []string{test.source}, queue.sources)
		})
	}
}

func (*concurrencyQueue) Heartbeat(context.Context, *codexcontrol.ClaimedControl) error { return nil }

func (q *concurrencyQueue) Complete(context.Context, *codexcontrol.ClaimedControl, codexcontrol.TurnResult) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.completed++
	return nil
}

func (*concurrencyQueue) Cancel(context.Context, *codexcontrol.ClaimedControl, string, string) error {
	return nil
}
func (*concurrencyQueue) Fail(context.Context, *codexcontrol.ClaimedControl, string, error) error {
	return nil
}
func (*concurrencyQueue) Reconcile(context.Context, *codexcontrol.ClaimedControl, string, error) error {
	return nil
}
func (*concurrencyQueue) ReplySatisfied(context.Context, *codexcontrol.ClaimedControl) (bool, error) {
	return true, nil
}
func (*concurrencyQueue) RequeueExpired(context.Context) (int64, error) { return 0, nil }

type blockingProcessor struct {
	mu        sync.Mutex
	started   chan struct{}
	release   chan struct{}
	active    int
	maxActive int
}

func (p *blockingProcessor) Process(context.Context, *codexcontrol.ClaimedControl) (codexcontrol.TurnResult, error) {
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
	return codexcontrol.TurnResult{}, nil
}

func TestRunnerLimitsConcurrentJobsToSix(t *testing.T) {
	jobs := make([]*codexcontrol.ClaimedControl, 7)
	for index := range jobs {
		jobs[index] = &codexcontrol.ClaimedControl{}
		jobs[index].ID = uuid.New()
		jobs[index].LeaseToken = "lease"
		jobs[index].LeaseEpoch = 1
	}
	jobQueue := &concurrencyQueue{jobs: jobs}
	processor := &blockingProcessor{started: make(chan struct{}, 7), release: make(chan struct{})}
	runner := &Runner{
		cfg:      config.Config{WorkerID: "worker", WorkerMaxConcurrentJobs: 6, HeartbeatInterval: time.Hour},
		controls: jobQueue, processor: processor, logger: zap.NewNop(),
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
