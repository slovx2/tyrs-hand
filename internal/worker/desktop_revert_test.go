package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestDesktopRevertUsesRollbackControlLifecycle(t *testing.T) {
	for _, method := range []string{"thread/rollback", "thread/revert"} {
		t.Run(method, func(t *testing.T) {
			workspaceID, reservationID := uuid.New(), uuid.New()
			params := json.RawMessage(`{"threadId":"thread","beforeTurnId":"latest"}`)
			response := json.RawMessage(`{"thread":{"id":"thread","turns":[]},"turnsBackwardsCursor":null,"itemsBackwardsCursor":null}`)
			var prepared, completed atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				unwrapWorkerTestRequest(t, r)
				switch r.URL.Path {
				case "/worker/v1/desktop-rollbacks":
					var request workerprotocol.DesktopRollbackPrepareRequest
					require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
					require.Equal(t, workspaceID, request.WorkspaceID)
					require.JSONEq(t, string(params), string(request.Params))
					prepared.Store(true)
					require.NoError(t, json.NewEncoder(w).Encode(workerprotocol.DesktopRollbackState{
						ID: reservationID, WorkspaceID: workspaceID, ThreadID: "thread", TargetTurnID: "latest", Status: "reserved",
					}))
				case "/worker/v1/desktop-rollbacks/" + reservationID.String() + "/complete":
					var request workerprotocol.DesktopRollbackCompleteRequest
					require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
					require.True(t, prepared.Load())
					require.Empty(t, request.Error)
					require.JSONEq(t, string(response), string(request.Response))
					completed.Store(true)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("意外请求 %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			store, err := newJournalStore(t.TempDir())
			require.NoError(t, err)
			processor := &Processor{cfg: config.Config{ControlTimeout: time.Second},
				journals:   store,
				client:     workerprotocol.NewClient(server.URL, "mock-only", time.Second),
				workspaces: &workspaceCodexRegistry{ctx: t.Context()}}
			controller := &desktopController{processor: processor, workspace: &workspaceCodex{runtime: workspaceRuntime{WorkspaceID: workspaceID}}}
			call := appserverhub.Call{Role: appserverhub.RoleDesktop, Method: method, Params: params}
			plan, err := controller.PrepareCall(context.Background(), call)
			require.NoError(t, err)
			require.True(t, plan.Forward)
			result, err := controller.CompleteCall(context.Background(), call, plan, response, nil)
			require.NoError(t, err)
			require.JSONEq(t, string(response), string(result))
			require.True(t, completed.Load())
		})
	}
}

type rollbackReadClient func(context.Context, string, any, any) error

func (f rollbackReadClient) Call(ctx context.Context, method string, params, result any) error {
	return f(ctx, method, params, result)
}

func TestRollbackReconciliationRequiresCompleteMatchingHistory(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		err       error
		applied   bool
	}{
		{name: "retained", raw: `{"thread":{"id":"thread","turns":[{"id":"latest"}]}}`},
		{name: "missing-turns", raw: `{"thread":{"id":"thread"}}`},
		{name: "null-turns", raw: `{"thread":{"id":"thread","turns":null}}`},
		{name: "wrong-thread", raw: `{"thread":{"id":"other","turns":[]}}`},
		{name: "read-failed", err: errors.New("offline")},
		{name: "applied", raw: `{"thread":{"id":"thread","turns":[{"id":"first"}]}}`, applied: true},
		{name: "empty-history", raw: `{"thread":{"id":"thread","turns":[]}}`, applied: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := rollbackReadClient(func(_ context.Context, method string, params, result any) error {
				calls++
				require.Equal(t, "thread/read", method, "对账不能重发回退")
				require.Equal(t, map[string]any{"threadId": "thread", "includeTurns": true}, params)
				if test.err != nil {
					return test.err
				}
				return json.Unmarshal([]byte(test.raw), result)
			})
			raw, applied := readDesktopRollbackApplied(t.Context(), client, workerprotocol.DesktopRollbackState{ThreadID: "thread", TargetTurnID: "latest"})
			require.Equal(t, test.applied, applied)
			require.Equal(t, 1, calls)
			if applied {
				require.JSONEq(t, test.raw, string(raw), "保留读回的完整响应，不返回空对象")
			}
		})
	}
}

func TestDesktopReplacementJournalSurvivesRestartAndPreventsDuplicateStart(t *testing.T) {
	root := t.TempDir()
	store, err := newJournalStore(root)
	require.NoError(t, err)
	state := workerprotocol.DesktopRollbackState{ID: uuid.New(), WorkspaceID: uuid.New(), ThreadID: "thread", TargetTurnID: "latest"}
	params := json.RawMessage(`{"threadId":"thread","input":[{"type":"text","text":"replacement"}]}`)
	var calls atomic.Int64
	var consumed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unwrapWorkerTestRequest(t, r)
		calls.Add(1)
		var request workerprotocol.DesktopTurnPreflightRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, state.WorkspaceID, request.WorkspaceID)
		prepared := request.Params
		if !consumed.Load() {
			var values map[string]any
			require.NoError(t, json.Unmarshal(prepared, &values))
			values["clientUserMessageId"] = state.ID.String()
			encoded, encodeErr := json.Marshal(values)
			require.NoError(t, encodeErr)
			prepared = encoded
		}
		require.NoError(t, json.NewEncoder(w).Encode(workerprotocol.DesktopTurnPreflightResponse{Params: prepared}))
	}))
	t.Cleanup(server.Close)
	processor := &Processor{cfg: config.Config{ControlTimeout: time.Second}, journals: store,
		client: workerprotocol.NewClient(server.URL, "mock-only", time.Second), workspaces: &workspaceCodexRegistry{ctx: t.Context()}}
	controller := &desktopController{processor: processor, workspace: &workspaceCodex{runtime: workspaceRuntime{WorkspaceID: state.WorkspaceID}}}
	ordinary, err := controller.preflightDesktopReplacement(t.Context(), params)
	require.NoError(t, err)
	require.JSONEq(t, string(params), string(ordinary))
	require.Zero(t, calls.Load(), "普通回合不能增加 Control 前置请求")
	require.NoError(t, store.saveDesktopRollback(desktopRollbackJournal{State: state, Applied: true}))
	processor.journals, err = newJournalStore(root)
	require.NoError(t, err)
	prepared, err := controller.preflightDesktopReplacement(t.Context(), params)
	require.NoError(t, err)
	require.NoError(t, controller.startDesktopReplacement(prepared))
	require.ErrorContains(t, controller.startDesktopReplacement(prepared), "不能重复执行")
	_, err = controller.preflightDesktopReplacement(t.Context(), params)
	require.ErrorContains(t, err, "替换回合已提交")
	consumed.Store(true)
	_, err = controller.preflightDesktopReplacement(t.Context(), params)
	require.NoError(t, err)
	pending, err := processor.journals.loadDesktopRollback(state.ThreadID)
	require.NoError(t, err)
	require.Nil(t, pending)
}

func TestDesktopRollbackUnknownWithoutRuntimeRemainsUnresolved(t *testing.T) {
	store, err := newJournalStore(t.TempDir())
	require.NoError(t, err)
	state := workerprotocol.DesktopRollbackState{ID: uuid.New(), WorkspaceID: uuid.New(), ThreadID: "thread", TargetTurnID: "latest"}
	require.NoError(t, store.saveDesktopRollback(desktopRollbackJournal{State: state}))
	controller := &desktopController{processor: &Processor{cfg: config.Config{ControlTimeout: time.Second},
		journals: store, workspaces: &workspaceCodexRegistry{ctx: t.Context()}}, workspace: &workspaceCodex{}}
	unknown := &codex.RequestError{Method: "thread/revert", State: codex.RequestUnknown, Cause: context.DeadlineExceeded}
	_, err = controller.completeDesktopRollback(state, nil, unknown)
	require.ErrorIs(t, err, unknown)
	pending, err := store.loadDesktopRollback(state.ThreadID)
	require.NoError(t, err)
	require.False(t, pending.Applied, "未知结果不能被登记为成功或撤销")
}
