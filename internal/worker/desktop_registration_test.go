package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func blockJournalDirectory(t *testing.T, store *journalStore) func() {
	t.Helper()
	backup := store.directory + "-fixture"
	require.NoError(t, os.Rename(store.directory, backup))
	require.NoError(t, os.WriteFile(store.directory, []byte("not a directory"), 0o600))
	var restored atomic.Bool
	restore := func() {
		if restored.Swap(true) {
			return
		}
		require.NoError(t, os.Remove(store.directory))
		require.NoError(t, os.Rename(backup, store.directory))
	}
	t.Cleanup(restore)
	return restore
}

func TestDesktopTurnRegistrationWaitsForDurableJournal(t *testing.T) {
	var calls atomic.Int64
	store, err := newJournalStore(t.TempDir())
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unwrapWorkerTestRequest(t, r)
		stored, err := store.loadAll()
		require.NoError(t, err)
		require.Len(t, stored, 1)
		require.NotNil(t, stored[0].DesktopRequest, "补登记之前必须保存完整请求")
		require.Equal(t, "turn", stored[0].Task.Claimed.ConfirmedTurnID)
		calls.Add(1)
		http.Error(w, "fixture stop", http.StatusNotFound)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	processor := &Processor{cfg: config.Config{ControlTimeout: time.Second}, logger: zap.NewNop(), journals: store,
		client: workerprotocol.NewClient(server.URL, "test", time.Second)}
	controller := &desktopController{processor: processor, workspace: &workspaceCodex{runtime: workspaceRuntime{WorkspaceID: uuid.New()}}}
	task := workerprotocol.Task{Snapshot: workerprotocol.TaskSnapshot{Runtime: workerprotocol.RuntimeSnapshot{Engine: "codex"}}}
	task.Claimed.RunID, task.Claimed.ID = uuid.New(), uuid.New()
	task.Claimed.ConfirmedTurnID = "turn"
	reporter, err := newDesktopEventReporter(ctx, processor, &task)
	require.NoError(t, err)
	defer reporter.stopFlushLoop()
	reporter.holdRegistration()
	restore := blockJournalDirectory(t, store)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		controller.registerDesktopTurn(ctx, json.RawMessage(`{"threadId":"thread"}`), strings.Repeat("a", 64), "turn", nil, "", &desktopCallState{task: &task, reporter: reporter})
	}()
	require.Never(t, func() bool { return calls.Load() > 0 }, 150*time.Millisecond, 10*time.Millisecond)
	restore()
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("恢复可写日志后没有继续补登记")
	}
	require.EqualValues(t, 1, calls.Load())
}

func TestDesktopReporterWaitsForDurableEventsAndTerminal(t *testing.T) {
	var events, completed atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unwrapWorkerTestRequest(t, r)
		if strings.HasSuffix(r.URL.Path, "/events") {
			events.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "/complete") {
			completed.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	store, err := newJournalStore(t.TempDir())
	require.NoError(t, err)
	processor := &Processor{cfg: config.Config{ControlTimeout: time.Second}, logger: zap.NewNop(), journals: store,
		client: workerprotocol.NewClient(server.URL, "test", time.Second)}
	task := workerprotocol.Task{Snapshot: workerprotocol.TaskSnapshot{Runtime: workerprotocol.RuntimeSnapshot{Engine: "codex"}}}
	task.Claimed.RunID = uuid.New()
	reporter, err := newDesktopEventReporter(ctx, processor, &task)
	require.NoError(t, err)
	defer reporter.stopFlushLoop()
	restore := blockJournalDirectory(t, store)
	reporter.Report("test", json.RawMessage(`{}`))
	finished := make(chan struct{})
	go func() { reporter.Finish(codexcontrol.TurnResult{}, nil); close(finished) }()
	require.Never(t, func() bool { return events.Load()+completed.Load() > 0 }, 150*time.Millisecond, 10*time.Millisecond)
	restore()
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("恢复可写日志后没有上传缓存事件和终态")
	}
	require.EqualValues(t, 1, events.Load())
	require.EqualValues(t, 1, completed.Load())
}

func TestDesktopTurnRegistrationWaitsForThread(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unwrapWorkerTestRequest(t, r)
		calls.Add(1)
		http.Error(w, "fixture failure after barrier", http.StatusNotFound)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	store, err := newJournalStore(t.TempDir())
	require.NoError(t, err)
	workspaceID := uuid.New()
	registration := &desktopThreadRegistration{done: make(chan struct{})}
	processor := &Processor{cfg: config.Config{ControlTimeout: time.Second}, logger: zap.NewNop(), journals: store,
		client:     workerprotocol.NewClient(server.URL, "test", time.Second),
		threadSync: map[string]*desktopThreadRegistration{workspaceID.String() + ":thread": registration}}
	controller := &desktopController{processor: processor, workspace: &workspaceCodex{runtime: workspaceRuntime{WorkspaceID: workspaceID}}}
	task := workerprotocol.Task{Snapshot: workerprotocol.TaskSnapshot{Runtime: workerprotocol.RuntimeSnapshot{Engine: "codex"}}}
	task.Claimed.RunID = uuid.New()
	task.Claimed.ID = uuid.New()
	reporter, err := newDesktopEventReporter(ctx, processor, &task)
	require.NoError(t, err)
	defer reporter.stopFlushLoop()
	reporter.holdRegistration()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		controller.registerDesktopTurn(ctx, json.RawMessage(`{"threadId":"thread"}`), strings.Repeat("a", 64), "turn", nil, "", &desktopCallState{task: &task, reporter: reporter})
	}()
	require.Never(t, func() bool { return calls.Load() > 0 }, 100*time.Millisecond, 10*time.Millisecond, "会话未登记前不能补报 Turn")
	close(registration.done)
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("登记未解除阻塞")
	}
	require.EqualValues(t, 1, calls.Load())
	require.True(t, reporter.journal.ControlAbandoned, "真正的登记失败仍保留 Journal，不自动重放")
}

func TestDesktopReporterDefersEventsAndTerminalUntilRegistration(t *testing.T) {
	var events, completed atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unwrapWorkerTestRequest(t, r)
		if strings.HasSuffix(r.URL.Path, "/events") {
			events.Add(1)
		} else if strings.HasSuffix(r.URL.Path, "/complete") {
			completed.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	store, err := newJournalStore(t.TempDir())
	require.NoError(t, err)
	p := &Processor{cfg: config.Config{ControlTimeout: time.Second}, logger: zap.NewNop(), journals: store,
		client: workerprotocol.NewClient(server.URL, "test", time.Second)}
	task := workerprotocol.Task{Snapshot: workerprotocol.TaskSnapshot{Runtime: workerprotocol.RuntimeSnapshot{Engine: "codex"}}}
	task.Claimed.RunID = uuid.New()
	reporter, err := newDesktopEventReporter(ctx, p, &task)
	require.NoError(t, err)
	reporter.holdRegistration()
	reporter.Report("test", json.RawMessage(`{}`))
	done := make(chan struct{})
	go func() { reporter.Finish(codexcontrol.TurnResult{}, nil); close(done) }()
	require.Never(t, func() bool { return events.Load()+completed.Load() > 0 }, 100*time.Millisecond, 10*time.Millisecond)
	reporter.finishRegistration()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("终态未继续补报")
	}
	require.EqualValues(t, 1, events.Load())
	require.EqualValues(t, 1, completed.Load())
}

func TestControlToolWaitsForConfirmedRegistration(t *testing.T) {
	for _, outcome := range []string{"confirmed", "failed", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			reporter := &desktopEventReporter{journal: &runJournal{}}
			reporter.holdRegistration()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- reporter.waitControlRegistration(ctx) }()
			select {
			case err := <-done:
				t.Fatalf("登记未确认时提前放行工具: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			switch outcome {
			case "confirmed":
				reporter.confirmRegistration()
			case "failed":
				reporter.finishRegistration()
			case "canceled":
				cancel()
			}
			select {
			case err := <-done:
				if outcome == "confirmed" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			case <-time.After(time.Second):
				t.Fatal("登记结果没有解除等待")
			}
		})
	}
}
