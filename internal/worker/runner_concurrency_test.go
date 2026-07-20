package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/devenv"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type concurrencyQueue struct {
	mu        sync.Mutex
	jobs      []*codexcontrol.ClaimedControl
	claimed   int
	completed int
}

func (q *concurrencyQueue) Claim(context.Context, string) (*codexcontrol.ClaimedControl, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.claimed == len(q.jobs) {
		return nil, nil
	}
	job := q.jobs[q.claimed]
	q.claimed++
	return job, nil
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

type failedDockerCleanup struct{}

func (failedDockerCleanup) Environment() []string { return nil }
func (failedDockerCleanup) Close(context.Context) error {
	return errors.New("container is in use")
}

func TestDockerCleanupFailureOnlyProducesWarning(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	processor := &Processor{cfg: config.Config{DockerCleanupTimeout: time.Second}, logger: zap.New(core)}
	claimed := &codexcontrol.ClaimedControl{RunID: uuid.New()}
	claimed.ID = uuid.New()
	processor.finishHostDocker(failedDockerCleanup{}, claimed)
	require.Len(t, logs.All(), 1)
	require.Contains(t, logs.All()[0].Message, "自动停止失败")
}
