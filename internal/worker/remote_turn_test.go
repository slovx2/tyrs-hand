package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

type snapshotRuntimeClient struct {
	snapshot codex.ThreadSnapshot
}

func (c snapshotRuntimeClient) Call(_ context.Context, method string, _ any, result any) error {
	if method != "thread/read" {
		return fmt.Errorf("未预期的 Codex 方法: %s", method)
	}
	payload, err := json.Marshal(map[string]any{"thread": c.snapshot})
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, result)
}

func TestRemoteTurnInterruptedTerminalStatusesAreUserInterrupts(t *testing.T) {
	for _, status := range []string{"interrupted", "cancelled", "canceled"} {
		err := remoteTurnTerminalError("快照", status)
		require.ErrorIs(t, err, errRemoteInterrupt)
	}
	require.False(t, errors.Is(remoteTurnTerminalError("快照", "failed"), errRemoteInterrupt))
}

func TestWaitRemoteTurnMapsInterruptedEvent(t *testing.T) {
	processor := &RemoteProcessor{cfg: config.Config{
		TurnMaxDuration: time.Second, TurnIdleTimeout: time.Second,
		CodexStatusPollInterval: time.Hour,
	}}
	threadID, turnID := "thread-1", "turn-1"
	task := remoteTurnTask()
	events := make(chan codex.Event, 1)
	events <- codex.Event{Method: "turn/completed", Params: json.RawMessage(fmt.Sprintf(
		`{"threadId":%q,"turn":{"id":%q,"status":"interrupted"}}`, threadID, turnID))}

	_, err := processor.waitRemoteTurn(context.Background(), nil, events, &task,
		threadID, turnID, make(chan workerprotocol.RunCommand), nil, nil)
	require.ErrorIs(t, err, errRemoteInterrupt)
}

func TestWaitRemoteTurnMapsInterruptedSnapshot(t *testing.T) {
	processor := &RemoteProcessor{cfg: config.Config{
		TurnMaxDuration: time.Second, TurnIdleTimeout: time.Second,
		CodexStatusPollInterval: time.Millisecond,
	}}
	threadID, turnID := "thread-1", "turn-1"
	task := remoteTurnTask()
	runtime := codex.NewRuntime(snapshotRuntimeClient{snapshot: codex.ThreadSnapshot{
		ID: threadID, Turns: []codex.TurnSnapshot{{ID: turnID, Status: "interrupted"}},
	}})

	_, err := processor.waitRemoteTurn(context.Background(), runtime, make(chan codex.Event),
		&task, threadID, turnID, make(chan workerprotocol.RunCommand), nil, nil)
	require.ErrorIs(t, err, errRemoteInterrupt)
}

func TestWaitRemoteTurnStopsOnNonRetryableCodexError(t *testing.T) {
	processor := &RemoteProcessor{cfg: config.Config{
		TurnMaxDuration: time.Second, TurnIdleTimeout: time.Second,
		CodexStatusPollInterval: time.Hour,
	}}
	threadID, turnID := "thread-1", "turn-1"
	task := remoteTurnTask()
	events := make(chan codex.Event, 1)
	events <- codex.Event{Method: "error", Params: json.RawMessage(fmt.Sprintf(
		`{"threadId":%q,"turnId":%q,"willRetry":false,"error":{"message":"at capacity","codexErrorInfo":"serverOverloaded","additionalDetails":"try later"}}`,
		threadID, turnID))}

	_, err := processor.waitRemoteTurn(context.Background(), nil, events, &task,
		threadID, turnID, make(chan workerprotocol.RunCommand), nil, nil)
	var codexErr *workerprotocol.CodexTurnError
	require.ErrorAs(t, err, &codexErr)
	require.Equal(t, "at capacity", codexErr.Message)
	require.JSONEq(t, `"serverOverloaded"`, string(codexErr.CodexErrorInfo))
	require.False(t, codexErr.WillRetry)
	require.Equal(t, threadID, codexErr.ThreadID)
	require.Equal(t, turnID, codexErr.TurnID)
}

func TestWaitRemoteTurnContinuesAfterRetryableCodexError(t *testing.T) {
	processor := &RemoteProcessor{cfg: config.Config{
		TurnMaxDuration: time.Second, TurnIdleTimeout: time.Second,
		CodexStatusPollInterval: time.Hour,
	}}
	threadID, turnID := "thread-1", "turn-1"
	task := remoteTurnTask()
	events := make(chan codex.Event, 3)
	events <- codex.Event{Method: "error", Params: json.RawMessage(fmt.Sprintf(
		`{"threadId":%q,"turnId":%q,"willRetry":true,"error":{"message":"reconnecting"}}`,
		threadID, turnID))}
	events <- codex.Event{Method: "item/completed", Params: json.RawMessage(fmt.Sprintf(
		`{"threadId":%q,"turnId":%q,"item":{"type":"agentMessage","phase":"final_answer","text":"done"}}`,
		threadID, turnID))}
	events <- codex.Event{Method: "turn/completed", Params: json.RawMessage(fmt.Sprintf(
		`{"threadId":%q,"turn":{"id":%q,"status":"completed"}}`, threadID, turnID))}

	result, err := processor.waitRemoteTurn(context.Background(), nil, events, &task,
		threadID, turnID, make(chan workerprotocol.RunCommand), nil, nil)
	require.NoError(t, err)
	require.Equal(t, "done", result.FinalAnswer)
}

func TestWaitRemoteTurnReadsCodexErrorFromFailedSnapshot(t *testing.T) {
	processor := &RemoteProcessor{cfg: config.Config{
		TurnMaxDuration: time.Second, TurnIdleTimeout: time.Second,
		CodexStatusPollInterval: time.Millisecond,
	}}
	threadID, turnID := "thread-1", "turn-1"
	task := remoteTurnTask()
	runtime := codex.NewRuntime(snapshotRuntimeClient{snapshot: codex.ThreadSnapshot{
		ID: threadID, Turns: []codex.TurnSnapshot{{ID: turnID, Status: "failed",
			Error: &codex.TurnError{Message: "bad request",
				CodexErrorInfo: json.RawMessage(`"badRequest"`)}}},
	}})

	_, err := processor.waitRemoteTurn(context.Background(), runtime, make(chan codex.Event),
		&task, threadID, turnID, make(chan workerprotocol.RunCommand), nil, nil)
	var codexErr *workerprotocol.CodexTurnError
	require.ErrorAs(t, err, &codexErr)
	require.Equal(t, "bad request", codexErr.Message)
}

func TestReportRuntimeSettingsAppliedUsesAcceptedSnapshot(t *testing.T) {
	var eventType string
	var payload json.RawMessage
	reportRuntimeSettingsApplied(func(kind string, value json.RawMessage) {
		eventType, payload = kind, value
	}, workerprotocol.RuntimeSnapshot{Model: "gpt-5.6-terra", ReasoningEffort: "xhigh",
		ServiceTier: "fast", CollaborationMode: "plan", SettingsRevision: 7}, "turn/start")
	require.Equal(t, "runtime.settings_applied", eventType)
	var applied workerprotocol.RuntimeSettingsApplied
	require.NoError(t, json.Unmarshal(payload, &applied))
	require.Equal(t, "turn/start", applied.Phase)
	require.Equal(t, "gpt-5.6-terra", applied.Model)
	require.Equal(t, "xhigh", applied.ReasoningEffort)
	require.Equal(t, "priority", applied.ServiceTier)
	require.Equal(t, "plan", applied.CollaborationMode)
	require.EqualValues(t, 7, applied.SettingsRevision)
}

func remoteTurnTask() workerprotocol.Task {
	return workerprotocol.Task{Claimed: codexcontrol.ClaimedControl{
		Intent: codexcontrol.Intent{ID: uuid.New()},
	}}
}
