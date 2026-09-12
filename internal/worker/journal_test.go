package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestJournalPersistsAndLocksWorkerData(t *testing.T) {
	root := t.TempDir()
	store, err := newJournalStore(root)
	require.NoError(t, err)
	runID := uuid.New()
	journal := &runJournal{Task: workerprotocol.Task{}, NextSequence: 2,
		PendingEvents: []workerprotocol.EventInput{{Sequence: 1, Type: "turn/started"}}}
	journal.Task.Claimed.RunID = runID
	require.NoError(t, store.save(journal))

	loaded, err := store.loadAll()
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, runID, loaded[0].Task.Claimed.RunID)
	require.Equal(t, int64(1), loaded[0].PendingEvents[0].Sequence)
	info, err := os.Stat(store.path(runID))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	lock, err := AcquireDataLock(root)
	require.NoError(t, err)
	_, err = AcquireDataLock(root)
	require.ErrorContains(t, err, "已经有 Worker")
	require.NoError(t, lock.Close())
	second, err := AcquireDataLock(root)
	require.NoError(t, err)
	require.NoError(t, second.Close())

	require.NoError(t, store.remove(runID))
	_, err = os.Stat(filepath.Join(root, "control-state", "runs", runID.String()+".json"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestCredentialFileRequiresOwnerOnlyPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "credential")
	require.NoError(t, writeCredential(path, "node-secret"))
	value, err := readCredential(path)
	require.NoError(t, err)
	require.Equal(t, "node-secret", value)
	require.NoError(t, os.Chmod(path, 0o644))
	_, err = readCredential(path)
	require.ErrorContains(t, err, "0600")
}

func TestJournalKeepsEventsWhileControlIsUnavailableAndFlushesOnce(t *testing.T) {
	var available atomic.Bool
	var accepted atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		unwrapWorkerTestRequest(t, request)
		if !available.Load() {
			http.Error(response, "control unavailable", http.StatusServiceUnavailable)
			return
		}
		accepted.Add(1)
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	store, err := newJournalStore(t.TempDir())
	require.NoError(t, err)
	client := workerprotocol.NewClient(server.URL, "node-token", time.Second)
	runner := &Runner{cfg: config.Config{ControlTimeout: time.Second}, client: client,
		logger: zap.NewNop(), journals: store}
	journal := &runJournal{NextSequence: 2,
		PendingEvents: []workerprotocol.EventInput{{Sequence: 1, Type: "turn.started"}}}
	journal.Task.Claimed.RunID = uuid.New()
	journal.Task.Claimed.LeaseToken = "lease"
	journal.Task.Claimed.LeaseEpoch = 1
	require.NoError(t, store.save(journal))
	runner.flushEvents(context.Background(), journal, zap.NewNop())
	require.Len(t, journal.PendingEvents, 1)
	loaded, err := store.loadAll()
	require.NoError(t, err)
	require.Len(t, loaded[0].PendingEvents, 1)

	available.Store(true)
	runner.flushEvents(context.Background(), journal, zap.NewNop())
	require.Empty(t, journal.PendingEvents)
	require.EqualValues(t, 1, accepted.Load())
	loaded, err = store.loadAll()
	require.NoError(t, err)
	require.Empty(t, loaded[0].PendingEvents)
}

func TestDeliverTerminalKeepsPendingEventsAfterCompletion(t *testing.T) {
	var eventsAvailable atomic.Bool
	var acceptedEvents atomic.Int64
	var completed atomic.Int64
	var heartbeats atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		unwrapWorkerTestRequest(t, request)
		if strings.HasSuffix(request.URL.Path, "/complete") {
			completed.Add(1)
			response.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.HasSuffix(request.URL.Path, "/events") {
			if eventsAvailable.Load() {
				acceptedEvents.Add(1)
				response.WriteHeader(http.StatusNoContent)
				return
			}
			http.Error(response, "control unavailable", http.StatusServiceUnavailable)
			return
		}
		heartbeats.Add(1)
		http.Error(response, "run finished", http.StatusConflict)
	}))
	defer server.Close()
	store, err := newJournalStore(t.TempDir())
	require.NoError(t, err)
	runner := &Runner{cfg: config.Config{ControlTimeout: time.Second},
		client: workerprotocol.NewClient(server.URL, "node-token", time.Second),
		logger: zap.NewNop(), journals: store}
	journal := &runJournal{NextSequence: 2,
		PendingEvents: []workerprotocol.EventInput{{Sequence: 1, Type: "turn.started"}},
		Result:        &codexcontrol.TurnResult{FinalAnswer: "done"}}
	journal.Task.Claimed.RunID = uuid.New()
	journal.Task.Claimed.LeaseToken = "lease"
	journal.Task.Claimed.LeaseEpoch = 1
	require.NoError(t, store.save(journal))

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	runner.deliverTerminal(ctx, journal, zap.NewNop())
	require.EqualValues(t, 1, completed.Load())
	require.True(t, journal.TerminalDelivered)
	require.Len(t, journal.PendingEvents, 1)
	_, err = os.Stat(store.path(journal.Task.Claimed.RunID))
	require.NoError(t, err)
	loaded, err := store.loadAll()
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.True(t, loaded[0].TerminalDelivered)
	require.Len(t, loaded[0].PendingEvents, 1)

	eventsAvailable.Store(true)
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	var active sync.WaitGroup
	active.Add(1)
	runner.runJournal(context.Background(), loaded[0],
		make(chan workerprotocol.RunCommand, 16), slots, &active)
	active.Wait()
	require.EqualValues(t, 1, acceptedEvents.Load())
	require.EqualValues(t, 1, completed.Load(), "补发事件时不能重复提交终态")
	require.EqualValues(t, 1, heartbeats.Load(),
		"首次终态提交前只补报一次 Run，恢复已确认终态时不能重复同步")
	_, err = os.Stat(store.path(journal.Task.Claimed.RunID))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRegisterDesktopTurnDropsJournalOnNotFound(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		unwrapWorkerTestRequest(t, request)
		calls.Add(1)
		http.Error(response, "missing thread", http.StatusNotFound)
	}))
	defer server.Close()
	store, err := newJournalStore(t.TempDir())
	require.NoError(t, err)
	processor := &Processor{cfg: config.Config{ControlTimeout: time.Second},
		client: workerprotocol.NewClient(server.URL, "node-token", time.Second),
		logger: zap.NewNop(), journals: store}
	controller := &desktopController{processor: processor,
		workspace: &workspaceCodex{runtime: workspaceRuntime{WorkspaceID: uuid.New()}}}
	task := workerprotocol.Task{}
	task.Claimed.RunID = uuid.New()
	task.Claimed.ID = uuid.New()
	reporter, err := newDesktopEventReporter(context.Background(), processor, &task)
	require.NoError(t, err)
	controller.registerDesktopTurn(context.Background(), json.RawMessage(`{"threadId":"local"}`),
		strings.Repeat("a", 64), "turn-1", nil, "", &desktopCallState{task: &task, reporter: reporter})
	require.EqualValues(t, 1, calls.Load())
	require.True(t, reporter.journal.ControlAbandoned)
	_, err = os.Stat(store.path(task.Claimed.RunID))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRegisterDesktopTurnRetriesBadGateway(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		unwrapWorkerTestRequest(t, request)
		calls.Add(1)
		http.Error(response, "bad gateway", http.StatusBadGateway)
	}))
	defer server.Close()
	store, err := newJournalStore(t.TempDir())
	require.NoError(t, err)
	processor := &Processor{cfg: config.Config{ControlTimeout: time.Second},
		client: workerprotocol.NewClient(server.URL, "node-token", time.Second),
		logger: zap.NewNop(), journals: store}
	controller := &desktopController{processor: processor,
		workspace: &workspaceCodex{runtime: workspaceRuntime{WorkspaceID: uuid.New()}}}
	task := workerprotocol.Task{}
	task.Claimed.RunID = uuid.New()
	task.Claimed.ID = uuid.New()
	reporter, err := newDesktopEventReporter(context.Background(), processor, &task)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3500*time.Millisecond)
	defer cancel()
	controller.registerDesktopTurn(ctx, json.RawMessage(`{"threadId":"local"}`),
		strings.Repeat("a", 64), "turn-1", nil, "", &desktopCallState{task: &task, reporter: reporter})
	require.GreaterOrEqual(t, calls.Load(), int64(2))
	require.False(t, reporter.journal.ControlAbandoned)
	_, err = os.Stat(store.path(task.Claimed.RunID))
	require.NoError(t, err)
}

func TestDeliverTerminalDropsUnboundDesktopJournal(t *testing.T) {
	var prepares atomic.Int64
	var completes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		unwrapWorkerTestRequest(t, request)
		if strings.Contains(request.URL.Path, "/complete") {
			completes.Add(1)
			http.Error(response, "missing run", http.StatusNotFound)
			return
		}
		prepares.Add(1)
		http.Error(response, "missing thread", http.StatusNotFound)
	}))
	defer server.Close()
	store, err := newJournalStore(t.TempDir())
	require.NoError(t, err)
	runner := &Runner{cfg: config.Config{ControlTimeout: time.Second},
		client: workerprotocol.NewClient(server.URL, "node-token", time.Second),
		logger: zap.NewNop(), journals: store}
	workspaceID := uuid.New()
	runID := uuid.New()
	journal := &runJournal{Result: &codexcontrol.TurnResult{FinalAnswer: "done"},
		DesktopRequest: &workerprotocol.DesktopTurnPrepareRequest{WorkspaceID: workspaceID,
			RunID: runID, IntentID: uuid.New(), RequestKey: strings.Repeat("b", 64),
			Params: json.RawMessage(`{"threadId":"local"}`)}}
	journal.Task.Claimed.RunID = runID
	journal.Task.Claimed.LeaseToken = "lease"
	journal.Task.Claimed.LeaseEpoch = 1
	require.NoError(t, store.save(journal))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	runner.deliverTerminal(ctx, journal, zap.NewNop())
	require.EqualValues(t, 1, prepares.Load())
	require.EqualValues(t, 0, completes.Load())
	require.True(t, journal.ControlAbandoned)
	_, err = os.Stat(store.path(runID))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestDeliverTerminalRetriesBadGateway(t *testing.T) {
	var completes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		unwrapWorkerTestRequest(t, request)
		if strings.HasSuffix(request.URL.Path, "/complete") {
			completes.Add(1)
			http.Error(response, "bad gateway", http.StatusBadGateway)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	store, err := newJournalStore(t.TempDir())
	require.NoError(t, err)
	runner := &Runner{cfg: config.Config{ControlTimeout: time.Second},
		client: workerprotocol.NewClient(server.URL, "node-token", time.Second),
		logger: zap.NewNop(), journals: store}
	journal := &runJournal{Result: &codexcontrol.TurnResult{FinalAnswer: "done"}}
	journal.Task.Claimed.RunID = uuid.New()
	journal.Task.Claimed.LeaseToken = "lease"
	journal.Task.Claimed.LeaseEpoch = 1
	require.NoError(t, store.save(journal))
	ctx, cancel := context.WithTimeout(context.Background(), 3500*time.Millisecond)
	defer cancel()
	runner.deliverTerminal(ctx, journal, zap.NewNop())
	require.GreaterOrEqual(t, completes.Load(), int64(2))
	require.False(t, journal.ControlAbandoned)
	_, err = os.Stat(store.path(journal.Task.Claimed.RunID))
	require.NoError(t, err)
}

func TestFinishDesktopTurnDropsJournalOnForbidden(t *testing.T) {
	var completes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		unwrapWorkerTestRequest(t, request)
		completes.Add(1)
		http.Error(response, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()
	store, err := newJournalStore(t.TempDir())
	require.NoError(t, err)
	processor := &Processor{cfg: config.Config{ControlTimeout: time.Second},
		client: workerprotocol.NewClient(server.URL, "node-token", time.Second),
		logger: zap.NewNop(), journals: store}
	task := workerprotocol.Task{}
	task.Claimed.RunID = uuid.New()
	reporter, err := newDesktopEventReporter(context.Background(), processor, &task)
	require.NoError(t, err)
	reporter.Finish(codexcontrol.TurnResult{FinalAnswer: "done"}, nil)
	require.EqualValues(t, 1, completes.Load())
	require.True(t, reporter.journal.ControlAbandoned)
	_, err = os.Stat(store.path(task.Claimed.RunID))
	require.ErrorIs(t, err, os.ErrNotExist)
}
