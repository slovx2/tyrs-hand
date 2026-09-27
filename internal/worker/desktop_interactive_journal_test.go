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
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func offlineApprovalFixture(t *testing.T) (*desktopController, *runJournal, codex.ServerRequest, workerprotocol.InteractiveAnswerRequest) {
	t.Helper()
	store, err := newJournalStore(t.TempDir())
	require.NoError(t, err)
	workspace := uuid.New()
	task := coordinatorTask(uuid.New(), uuid.New(), workspace, "thread", 3)
	task.Snapshot.Runtime.Engine = runtimeidentity.Claude
	task.Claimed.ConfirmedTurnID = "turn"
	journal := &runJournal{Task: task, NextSequence: 1, AppServerGeneration: 7}
	processor := &Processor{journals: store, coordinator: newRunCoordinator(store),
		logger: zap.NewNop(), cfg: config.Config{ControlTimeout: time.Second}}
	processor.runtimeIdentity.Engine = runtimeidentity.Claude
	processor.coordinator.register(journal, nil)
	controller := &desktopController{processor: processor, workspace: &workspaceCodex{
		generation: 7, runtime: workspaceRuntime{WorkspaceID: workspace}}}
	request := codex.ServerRequest{ID: json.RawMessage(`"approval"`),
		Method: "item/commandExecution/requestApproval",
		Params: json.RawMessage(`{"threadId":"thread","turnId":"turn","itemId":"item"}`)}
	answer := workerprotocol.InteractiveAnswerRequest{RequestID: request.ID,
		AppServerGeneration: 7, WorkspaceID: workspace, ThreadID: "thread", TurnID: "turn", ItemID: "item",
		Surface: "desktop", Answer: json.RawMessage(`{"decision":"accept"}`)}
	return controller, journal, request, answer
}

func TestOfflineApprovalRequiresDurableExactLiveScope(t *testing.T) {
	for _, test := range []string{"disk_failure", "wrong_engine", "wrong_turn", "wrong_workspace", "old_generation", "canceled", "invalid_answer", "finished"} {
		t.Run(test, func(t *testing.T) {
			controller, journal, request, answer := offlineApprovalFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch test {
			case "disk_failure":
				blocker := filepath.Join(t.TempDir(), "file")
				require.NoError(t, os.WriteFile(blocker, []byte("block"), 0o600))
				controller.processor.journals.directory = blocker
			case "wrong_engine":
				controller.processor.runtimeIdentity.Engine = runtimeidentity.Codex
			case "wrong_turn":
				answer.TurnID = "other"
			case "wrong_workspace":
				answer.WorkspaceID = uuid.New()
			case "old_generation":
				controller.workspace.generation = 8
			case "canceled":
				cancel()
			case "invalid_answer":
				answer.Answer = json.RawMessage(`{"decision":"invented-allow"}`)
			case "finished":
				journal.Result = &codexcontrol.TurnResult{FinalAnswer: "done"}
			}
			raw, err := controller.persistOfflineInteractive(ctx, request, answer)
			require.Error(t, err)
			require.Nil(t, raw)
			require.Empty(t, journal.InteractiveAnswers)
		})
	}
}

func TestOfflineApprovalMatchesUniqueThreadTurnWhenTurnUnknown(t *testing.T) {
	for _, test := range []string{"turn_not_observed", "answer_without_turn", "ambiguous"} {
		t.Run(test, func(t *testing.T) {
			controller, journal, request, answer := offlineApprovalFixture(t)
			coordinator := controller.processor.coordinator
			switch test {
			case "turn_not_observed":
				// 审批可能早于原生 turn/started 到达，此时活动回合尚无 ID。
				coordinator.mu.Lock()
				coordinator.active[journal.Task.Claimed.RunID].turnID = ""
				coordinator.mu.Unlock()
			case "answer_without_turn":
				answer.TurnID = ""
			case "ambiguous":
				answer.TurnID = ""
				other := coordinatorTask(uuid.New(), uuid.New(), answer.WorkspaceID, "thread", 3)
				other.Snapshot.Runtime.Engine = runtimeidentity.Claude
				coordinator.register(&runJournal{Task: other, NextSequence: 1, AppServerGeneration: 7}, nil)
			}
			raw, err := controller.persistOfflineInteractive(t.Context(), request, answer)
			if test == "ambiguous" {
				require.Error(t, err, "同一 Thread 有多个候选回合时不能猜测归属")
				require.Empty(t, journal.InteractiveAnswers)
				return
			}
			require.NoError(t, err)
			require.JSONEq(t, `{"decision":"accept"}`, string(raw))
			require.Len(t, journal.InteractiveAnswers, 1)
		})
	}
}

func TestOfflineUserInputCannotAnswerUndeclaredQuestions(t *testing.T) {
	params := json.RawMessage(`{"questions":[{"id":"known"}]}`)
	for _, answer := range []string{
		`{"answers":{"other":{"answers":["yes"]}}}`,
		`{"answers":{"known":{"answers":[]}}}`,
		`{"answers":{"known":{"answers":[" "]}}}`,
	} {
		_, err := normalizeOfflineAnswer("item/tool/requestUserInput", params, json.RawMessage(answer))
		require.Error(t, err)
	}
	_, err := normalizeOfflineAnswer("item/tool/requestUserInput", params,
		json.RawMessage(`{"answers":{"known":{"answers":["自由文本"]}}}`))
	require.NoError(t, err)
	// 空映射保留原生取消语义，不把未作答问题变成同意。
	_, err = normalizeOfflineAnswer("item/tool/requestUserInput", params, json.RawMessage(`{"answers":{}}`))
	require.NoError(t, err)
}

func TestOfflineApprovalReloadReportsConflictAndDeliversRealResult(t *testing.T) {
	for _, test := range []string{"accepted", "same_answer_replay", "remote_deny", "interrupted", "wrong_generation", "register_rejected"} {
		t.Run(test, func(t *testing.T) {
			controller, journal, request, answer := offlineApprovalFixture(t)
			_, err := controller.persistOfflineInteractive(t.Context(), request, answer)
			require.NoError(t, err)
			journal.Result = &codexcontrol.TurnResult{FinalAnswer: "真实工具已完成"}
			store := controller.processor.journals
			require.NoError(t, store.save(journal))
			loaded, err := store.loadAll()
			require.NoError(t, err)
			require.Len(t, loaded, 1)
			journal = loaded[0]
			var mu sync.Mutex
			var paths []string
			var events []workerprotocol.EventInput
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				unwrapWorkerTestRequest(t, r)
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				if strings.HasSuffix(r.URL.Path, "/interactive") && test == "register_rejected" {
					http.Error(w, "stale", http.StatusConflict)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/interactive/answer") {
					state := workerprotocol.InteractiveState{ID: uuid.New(), RequestID: request.ID,
						Method: request.Method, AppServerGeneration: 7, Status: "resolved",
						Answer: json.RawMessage(`{ "decision" : "accept" }`), Accepted: test == "accepted"}
					switch test {
					case "remote_deny":
						state.Answer = json.RawMessage(`{"decision":"decline"}`)
						state.Surface = "discord"
					case "interrupted":
						state.Status, state.Answer = "interrupted", nil
					case "wrong_generation":
						state.AppServerGeneration = 8
					}
					require.NoError(t, json.NewEncoder(w).Encode(state))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/fail") {
					t.Errorf("冲突不能把已真实执行的 Run 改判为失败")
				}
				if strings.HasSuffix(r.URL.Path, "/events") {
					var input workerprotocol.EventsRequest
					require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
					mu.Lock()
					events = append(events, input.Events...)
					mu.Unlock()
				}
				_, _ = w.Write([]byte(`{}`))
			}))
			t.Cleanup(server.Close)
			runner := &runtimeExecutor{cfg: config.Config{ControlTimeout: time.Second},
				client: workerprotocol.NewClient(server.URL, "test-only", time.Second),
				logger: zap.NewNop(), journals: store}
			// 没有 Processor 或 SDK 的恢复器只能补报，任何模型重放都会立即失败。
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			runner.deliverTerminal(ctx, journal, zap.NewNop())
			mu.Lock()
			defer mu.Unlock()
			joined := strings.Join(paths, "\n")
			if test == "accepted" || test == "same_answer_replay" {
				require.Less(t, strings.Index(joined, "/interactive\n"), strings.Index(joined, "/interactive/answer"))
				require.Less(t, strings.Index(joined, "/interactive/answer"), strings.Index(joined, "/complete"))
				require.True(t, journal.TerminalDelivered)
				require.NoFileExists(t, store.path(journal.Task.Claimed.RunID))
				return
			}
			// SSH 本地答案已生效：冲突只追加事件，事件先于真实终态送达，随后清理 Journal。
			require.NotContains(t, joined, "/fail")
			require.Less(t, strings.LastIndex(joined, "/events"), strings.Index(joined, "/complete"))
			require.True(t, journal.TerminalDelivered)
			require.NoFileExists(t, store.path(journal.Task.Claimed.RunID))
			require.NotEmpty(t, journal.InteractiveAnswers[0].Conflict)
			var conflicts []workerprotocol.EventInput
			for _, event := range events {
				if event.Type == offlineInteractiveConflictEvent {
					conflicts = append(conflicts, event)
				}
			}
			require.Len(t, conflicts, 1, "每个冲突恰好记录一次")
			var payload struct {
				Conflict    string          `json:"conflict"`
				LocalAnswer json.RawMessage `json:"localAnswer"`
				Remote      *struct {
					Answer json.RawMessage `json:"answer"`
				} `json:"remote"`
			}
			require.NoError(t, json.Unmarshal(conflicts[0].Payload, &payload))
			require.Equal(t, journal.InteractiveAnswers[0].Conflict, payload.Conflict)
			require.JSONEq(t, `{"decision":"accept"}`, string(payload.LocalAnswer))
			if test == "remote_deny" {
				require.JSONEq(t, `{"decision":"decline"}`, string(payload.Remote.Answer))
			}
		})
	}
}
