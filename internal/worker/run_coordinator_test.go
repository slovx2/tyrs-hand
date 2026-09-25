package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestDesktopThreadStartDoesNotWaitForControl(t *testing.T) {
	workspaceID := uuid.New()
	controller := &desktopController{processor: &Processor{}, workspace: &workspaceCodex{
		runtime: workspaceRuntime{WorkspaceID: workspaceID},
	}}
	params := json.RawMessage(`{"cwd":"/tmp/project"}`)
	plan, err := controller.PrepareCall(context.Background(), appserverhub.Call{
		Role: appserverhub.RoleDesktop, Method: "thread/start", Params: params,
	})
	require.NoError(t, err)
	require.True(t, plan.Forward)
	state, ok := plan.State.(*desktopThreadCallState)
	require.True(t, ok)
	require.Equal(t, workspaceID, state.request.WorkspaceID)
	require.Equal(t, "start", state.request.Operation)
	require.NotEmpty(t, state.request.RequestKey)
}

func TestRunCoordinatorRoutesAndPersistsInputsExactlyOnce(t *testing.T) {
	store, err := newJournalStore(t.TempDir())
	require.NoError(t, err)
	coordinator := newRunCoordinator(store)
	active := coordinatorTask(uuid.New(), uuid.New(), uuid.New(), "thread-1", 3)
	journal := &runJournal{Task: active, NextSequence: 1}
	require.NoError(t, store.save(journal))
	commands := make(chan workerprotocol.RunCommand, 3)
	coordinator.register(journal, commands)

	input := coordinatorTask(uuid.New(), active.Claimed.ControlID,
		active.Snapshot.Session.Project.WorkspaceID, "thread-1", 3)
	input.Claimed.Sequence = 2
	input.Claimed.Instruction = "第二条消息"
	routedTask, routed, applied := coordinator.route(&input)
	require.True(t, routed)
	require.False(t, applied)
	require.Equal(t, active.Claimed.RunID, routedTask.Claimed.RunID)
	command := <-commands
	require.Equal(t, input.Claimed.ID, command.ID)
	require.Equal(t, "第二条消息", command.Instruction)

	_, routed, applied = coordinator.route(&input)
	require.True(t, routed, "响应丢失时应保留原 reservation")
	require.False(t, applied)
	require.Empty(t, commands, "同一输入不能重复 steer")

	coordinator.markApplied(active.Claimed.RunID, input.Claimed.ID, "steer", "turn-1")
	_, routed, applied = coordinator.route(&input)
	require.True(t, routed)
	require.True(t, applied, "重连后只需补 ACK")
	require.Empty(t, commands)

	loaded, err := store.loadAll()
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, []appliedInputDecision{{InputID: input.Claimed.ID,
		Action: "steer", TurnID: "turn-1"}}, loaded[0].AppliedInputs)
}

func TestRunCoordinatorRejectsCrossEngineEvenWhenIDsCollide(t *testing.T) {
	coordinator := newRunCoordinator(nil)
	active := coordinatorTask(uuid.New(), uuid.New(), uuid.New(), "same-thread", 3)
	commands := make(chan workerprotocol.RunCommand, 1)
	coordinator.register(&runJournal{Task: active}, commands)
	input := active
	input.Snapshot.Runtime.Engine = "claude-code"
	_, routed, applied := coordinator.route(&input)
	require.False(t, routed)
	require.False(t, applied)
	require.Empty(t, commands)
}

func TestRunCoordinatorRejectLeavesInputForNextAttempt(t *testing.T) {
	coordinator := newRunCoordinator(nil)
	active := coordinatorTask(uuid.New(), uuid.New(), uuid.New(), "thread-1", 2)
	journal := &runJournal{Task: active, NextSequence: 1}
	commands := make(chan workerprotocol.RunCommand, 2)
	coordinator.register(journal, commands)
	input := coordinatorTask(uuid.New(), active.Claimed.ControlID,
		active.Snapshot.Session.Project.WorkspaceID, "thread-1", 2)

	_, routed, _ := coordinator.route(&input)
	require.True(t, routed)
	<-commands
	coordinator.reject(active.Claimed.RunID, input.Claimed.ID)
	_, routed, applied := coordinator.route(&input)
	require.True(t, routed)
	require.False(t, applied)
	require.Equal(t, input.Claimed.ID, (<-commands).ID)
}

func TestRunCoordinatorEnforcesSteerLimitAndThreadIsolation(t *testing.T) {
	coordinator := newRunCoordinator(nil)
	workspaceID, controlID := uuid.New(), uuid.New()
	active := coordinatorTask(uuid.New(), controlID, workspaceID, "thread-1", 1)
	journal := &runJournal{Task: active, NextSequence: 1}
	commands := make(chan workerprotocol.RunCommand, 2)
	coordinator.register(journal, commands)

	first := coordinatorTask(uuid.New(), controlID, workspaceID, "thread-1", 1)
	_, routed, _ := coordinator.route(&first)
	require.True(t, routed)
	<-commands
	coordinator.markApplied(active.Claimed.RunID, first.Claimed.ID, "steer", "turn-1")

	second := coordinatorTask(uuid.New(), controlID, workspaceID, "thread-1", 1)
	_, routed, _ = coordinator.route(&second)
	require.True(t, routed, "达到上限仍归属当前会话，不能启动并行 Turn")
	require.Empty(t, commands, "达到上限后输入应留到下一轮")

	other := coordinatorTask(uuid.New(), uuid.New(), workspaceID, "thread-2", 1)
	_, routed, _ = coordinator.route(&other)
	require.False(t, routed, "不同 Thread 的消息不能串入当前 Turn")

	interrupt := second
	interrupt.Claimed.ID = uuid.New()
	interrupt.Claimed.Operation = "interrupt"
	_, routed, _ = coordinator.route(&interrupt)
	require.True(t, routed, "停止请求不受 steer 数量限制")
}

func TestRunCoordinatorKeepsFullCommandQueueAttachedToActiveTurn(t *testing.T) {
	coordinator := newRunCoordinator(nil)
	active := coordinatorTask(uuid.New(), uuid.New(), uuid.New(), "busy-thread", 5)
	commands := make(chan workerprotocol.RunCommand, 1)
	coordinator.register(&runJournal{Task: active}, commands)
	first := active
	first.Claimed.ID = uuid.New()
	_, routed, _ := coordinator.route(&first)
	require.True(t, routed)
	next := first
	next.Claimed.ID = uuid.New()
	owner, routed, applied := coordinator.route(&next)
	require.True(t, routed, "队列满仍属于活动 Turn，Runner 不能另起同会话任务")
	require.False(t, applied)
	require.Equal(t, active.Claimed.RunID, owner.Claimed.RunID)
	require.Equal(t, first.Claimed.ID, (<-commands).ID)
	_, routed, _ = coordinator.route(&next)
	require.True(t, routed)
	require.Equal(t, next.Claimed.ID, (<-commands).ID, "释放队列后原输入可继续投递")
}

func TestRunStateSyncReplaysAppliedInputDecisionAfterAckLoss(t *testing.T) {
	runID, primaryID, steerID := uuid.New(), uuid.New(), uuid.New()
	var actions []workerprotocol.InputDecisionRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/worker/v1/inputs/decide":
			var decision workerprotocol.InputDecisionRequest
			require.NoError(t, json.NewDecoder(request.Body).Decode(&decision))
			actions = append(actions, decision)
			writer.WriteHeader(http.StatusNoContent)
		case "/worker/v1/runs/" + runID.String() + "/heartbeat":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"commands":[],"recovery":{}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	task := coordinatorTask(primaryID, uuid.New(), uuid.New(), "thread-1", 3)
	task.Claimed.RunID = runID
	journal := &runJournal{Task: task, AppliedInputs: []appliedInputDecision{{
		InputID: steerID, Action: "steer", TurnID: "turn-1",
	}}}
	runner := &runtimeExecutor{cfg: config.Config{ControlTimeout: time.Second},
		client: workerprotocol.NewClient(server.URL, "credential", time.Second),
		logger: zap.NewNop()}
	require.NoError(t, runner.syncRunState(context.Background(), journal, nil, zap.NewNop()))
	require.Len(t, actions, 2)
	require.Equal(t, primaryID, actions[0].InputID)
	require.Equal(t, "start", actions[0].Action)
	require.Equal(t, steerID, actions[1].InputID)
	require.Equal(t, "steer", actions[1].Action)
	require.Equal(t, "turn-1", actions[1].TurnID)
}

func coordinatorTask(intentID, controlID, workspaceID uuid.UUID, threadID string,
	maxSteers int,
) workerprotocol.Task {
	return workerprotocol.Task{Claimed: codexcontrol.ClaimedControl{
		Intent: codexcontrol.Intent{ID: intentID, ControlID: controlID,
			Operation: "turn_input"},
		RunID: uuid.New(), ExternalThreadID: threadID,
		MaxSteers: maxSteers,
	}, Snapshot: workerprotocol.TaskSnapshot{Runtime: workerprotocol.RuntimeSnapshot{Engine: "codex"}, Session: &workerprotocol.SessionSnapshot{
		Project: &workerprotocol.WorkspaceProjectContext{WorkspaceID: workspaceID},
	}}}
}
