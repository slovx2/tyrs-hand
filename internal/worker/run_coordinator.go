package worker

import (
	"sync"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
)

// runCoordinator 只描述 Worker 本机真实执行状态。Control 的状态不会参与
// 是否可以 start/steer 的判断。
type runCoordinator struct {
	mu     sync.Mutex
	store  *journalStore
	active map[uuid.UUID]*localRun
}

type localRun struct {
	journal   *runJournal
	commands  chan<- workerprotocol.RunCommand
	reserved  map[uuid.UUID]bool
	applied   map[uuid.UUID]bool
	runID     uuid.UUID
	inputID   uuid.UUID
	control   uuid.UUID
	workspace uuid.UUID
	thread    string
	turnID    string
	appends   int
	max       int
}

func newRunCoordinator(store *journalStore) *runCoordinator {
	return &runCoordinator{store: store, active: make(map[uuid.UUID]*localRun)}
}

func (c *runCoordinator) register(journal *runJournal,
	commands chan<- workerprotocol.RunCommand,
) {
	if c == nil || journal == nil || journal.Task.Claimed.RunID == uuid.Nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	applied := make(map[uuid.UUID]bool, len(journal.AppliedInputs))
	for _, decision := range journal.AppliedInputs {
		applied[decision.InputID] = true
	}
	maxAppend := journal.Task.Claimed.MaxSteers
	if maxAppend <= 0 {
		maxAppend = 5
	}
	workspaceID, threadID := localTaskScope(&journal.Task)
	c.active[journal.Task.Claimed.RunID] = &localRun{journal: journal,
		commands: commands, reserved: make(map[uuid.UUID]bool), applied: applied,
		runID: journal.Task.Claimed.RunID, inputID: journal.Task.Claimed.ID,
		control: journal.Task.Claimed.ControlID, workspace: workspaceID,
		thread: threadID, turnID: journal.Task.Claimed.ConfirmedTurnID,
		appends: len(applied), max: maxAppend}
}

func (c *runCoordinator) unregister(runID uuid.UUID) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.active, runID)
	c.mu.Unlock()
}

func (c *runCoordinator) route(task *workerprotocol.Task) (*workerprotocol.Task, bool, bool) {
	if c == nil || task == nil {
		return nil, false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, active := range c.active {
		if !sameLocalRun(active, task) {
			continue
		}
		result := localRunTask(active)
		if active.inputID == task.Claimed.ID {
			return result, true, false
		}
		if active.applied[task.Claimed.ID] {
			return result, true, true
		}
		if active.reserved[task.Claimed.ID] {
			return result, true, false
		}
		if task.Claimed.Operation == "turn_input" && active.appends >= active.max {
			return nil, false, false
		}
		command := workerprotocol.RunCommand{ID: task.Claimed.ID,
			Sequence: task.Claimed.Sequence, Operation: task.Claimed.Operation,
			Instruction: task.Claimed.Instruction, Session: task.Snapshot.Session,
			Discord: task.Snapshot.Discord}
		select {
		case active.commands <- command:
			active.reserved[task.Claimed.ID] = true
			active.appends++
			return result, true, false
		default:
			return nil, false, false
		}
	}
	return nil, false, false
}

func (c *runCoordinator) setTurnID(runID uuid.UUID, turnID string) {
	if c == nil || runID == uuid.Nil || turnID == "" {
		return
	}
	c.mu.Lock()
	if active := c.active[runID]; active != nil {
		active.turnID = turnID
	}
	c.mu.Unlock()
}

func (c *runCoordinator) markApplied(runID, inputID uuid.UUID, action, turnID string) {
	if c == nil || runID == uuid.Nil || inputID == uuid.Nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	active := c.active[runID]
	if active == nil {
		return
	}
	active.journal.mu.Lock()
	for _, decision := range active.journal.AppliedInputs {
		if decision.InputID == inputID {
			active.journal.mu.Unlock()
			return
		}
	}
	active.journal.AppliedInputs = append(active.journal.AppliedInputs, appliedInputDecision{
		InputID: inputID, Action: action, TurnID: turnID,
	})
	active.applied[inputID] = true
	delete(active.reserved, inputID)
	if c.store != nil {
		_ = c.store.save(active.journal)
	}
	active.journal.mu.Unlock()
}

func (c *runCoordinator) reject(runID, inputID uuid.UUID) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if active := c.active[runID]; active != nil {
		if active.reserved[inputID] {
			delete(active.reserved, inputID)
			if active.appends > len(active.applied) {
				active.appends--
			}
		}
	}
	c.mu.Unlock()
}

func sameLocalRun(left *localRun, right *workerprotocol.Task) bool {
	if left == nil || right == nil {
		return false
	}
	if left.control != uuid.Nil && left.control == right.Claimed.ControlID {
		return true
	}
	rightWorkspace, rightThread := localTaskScope(right)
	return left.workspace != uuid.Nil && left.workspace == rightWorkspace &&
		left.thread != "" && left.thread == rightThread
}

func localRunTask(run *localRun) *workerprotocol.Task {
	return &workerprotocol.Task{Claimed: codexcontrol.ClaimedControl{
		Intent: codexcontrol.Intent{ID: run.inputID, ControlID: run.control,
			ConfirmedTurnID: run.turnID},
		RunID: run.runID, ExternalThreadID: run.thread,
	}}
}

func localTaskScope(task *workerprotocol.Task) (uuid.UUID, string) {
	if task == nil {
		return uuid.Nil, ""
	}
	workspaceID := uuid.Nil
	if task.Snapshot.Session != nil && task.Snapshot.Session.Project != nil {
		workspaceID = task.Snapshot.Session.Project.WorkspaceID
	}
	return workspaceID, task.Claimed.ExternalThreadID
}
