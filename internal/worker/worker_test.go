package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestControlClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/internal/v1/tools/call":
			var input map[string]any
			require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
			require.Equal(t, "github", input["namespace"])
			_ = json.NewEncoder(response).Encode(codex.TextToolResult("ok", true))
		case "/internal/v1/git/credential":
			var input map[string]string
			require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
			switch input["purpose"] {
			case "fetch":
				_ = json.NewEncoder(response).Encode(map[string]string{"token": "temporary"})
			case "empty":
				_ = json.NewEncoder(response).Encode(map[string]string{})
			default:
				response.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(response).Encode(map[string]string{"detail": "denied"})
			}
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	client := NewControlClient(server.URL, time.Second)
	namespace := "github"
	result, err := client.CallTool(context.Background(), "capability", codex.ToolCallRequest{
		ThreadID: "thread", TurnID: "turn", CallID: "call", Namespace: &namespace,
		Tool: "issue_read", Arguments: json.RawMessage(`{"issue_number":1}`),
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	token, err := client.GitCredential(context.Background(), "capability", "fetch")
	require.NoError(t, err)
	require.Equal(t, "temporary", token)
	_, err = client.GitCredential(context.Background(), "capability", "empty")
	require.Error(t, err)
	_, err = client.GitCredential(context.Background(), "capability", "push")
	require.ErrorContains(t, err, "denied")
}

func TestProcessorHelpersAndLocalTools(t *testing.T) {
	threadID := "thread-1"
	turnID := "turn-1"
	started := json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1"}}`)
	require.True(t, eventBelongsToTurn(started, threadID, turnID, ""))
	require.False(t, eventBelongsToTurn(json.RawMessage(`{"broken":`), threadID, turnID, ""))
	matched, status := completedTurn(json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}`), threadID, turnID)
	require.True(t, matched)
	require.Equal(t, "completed", status)
	require.True(t, isActiveCodexTurnStatus("inProgress"))
	require.True(t, isActiveCodexTurnStatus("active"))
	require.True(t, isActiveCodexTurnStatus("running"))
	require.False(t, isActiveCodexTurnStatus("failed"))
	require.False(t, isActiveCodexTurnStatus("interrupted"))
	matched, _ = completedTurn(started, "other", turnID)
	require.False(t, matched)

	snapshot := codex.ThreadSnapshot{Turns: []codex.TurnSnapshot{{
		ID: turnID, Items: []codex.ItemSnapshot{{Type: "userMessage", ClientID: "steer-1"}},
	}}}
	applied, err := steerSnapshotApplied(snapshot, turnID, "steer-1")
	require.NoError(t, err)
	require.True(t, applied)
	applied, err = steerSnapshotApplied(snapshot, turnID, "missing")
	require.NoError(t, err)
	require.False(t, applied)
	_, err = steerSnapshotApplied(snapshot, "other-turn", "steer-1")
	require.ErrorContains(t, err, "其他 turn")

	root := t.TempDir()
	skillPath := filepath.Join(root, ".agents", "skills", "review", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(skillPath), 0o750))
	require.NoError(t, os.WriteFile(skillPath, []byte("# Review"), 0o600))
	skills, err := resolveSkills(root, []string{"review"})
	require.NoError(t, err)
	require.Equal(t, "review", skills[0].Name)
	canonicalSkillPath, err := filepath.EvalSymlinks(skillPath)
	require.NoError(t, err)
	require.Equal(t, canonicalSkillPath, skills[0].Path)
	_, err = resolveSkills(root, []string{"../escape"})
	require.Error(t, err)
	_, err = resolveSkills(root, []string{"missing"})
	require.Error(t, err)

	spec := localGitSpec()
	require.Equal(t, "git", spec.Name)
	require.Len(t, spec.Tools, 3)
	firstSignature := threadConfigSignature("provider", ports.ThreadOptions{Sandbox: "workspace-write", DynamicTools: []ports.DynamicToolSpec{spec}})
	require.Len(t, firstSignature, 64)
	require.Equal(t, firstSignature, threadConfigSignature("provider", ports.ThreadOptions{Sandbox: "workspace-write", DynamicTools: []ports.DynamicToolSpec{spec}}))
	require.NotEqual(t, firstSignature, threadConfigSignature("provider", ports.ThreadOptions{Sandbox: "danger-full-access", DynamicTools: []ports.DynamicToolSpec{spec}}))
	dockerOptions := ports.ThreadOptions{RuntimeConfig: codexRuntimeConfig([]string{
		"TYRS_HAND_DOCKER_WORKSPACE_ID=workspace", "TYRS_HAND_DOCKER_INTENT_ID=intent-1", "TYRS_HAND_DOCKER_RUN_ID=run-1",
	}, "/data/worker")}
	dockerSignature := threadConfigSignature("provider", dockerOptions)
	dockerOptions.RuntimeConfig = codexRuntimeConfig([]string{
		"TYRS_HAND_DOCKER_WORKSPACE_ID=workspace", "TYRS_HAND_DOCKER_INTENT_ID=intent-2", "TYRS_HAND_DOCKER_RUN_ID=run-2",
	}, "/data/worker")
	require.Equal(t, dockerSignature, threadConfigSignature("provider", dockerOptions))
	runtimeConfig := codexRuntimeConfig([]string{"PATH=/toolchain/bin:/usr/bin", "GOTOOLCHAIN=local"}, "/data/worker")
	policy := runtimeConfig["shell_environment_policy"].(map[string]any)
	require.Equal(t, "all", policy["inherit"])
	require.Equal(t, "/toolchain/bin:/usr/bin", policy["set"].(map[string]any)["PATH"])
	sandbox := runtimeConfig["sandbox_workspace_write"].(map[string]any)
	require.Equal(t, []string{"/data/worker/caches", "/data/worker/state"}, sandbox["writable_roots"])
	require.Contains(t, runtimeConfig, "hooks")
	require.Len(t, shortID(uuid.MustParse("12345678-1234-1234-1234-123456789012")), 8)

	workspace := &fakeWorkspace{status: "## main\n M file.go"}
	processor := &Processor{workspace: workspace}
	claimed := &codexcontrol.ClaimedControl{}
	statusResult, err := processor.executeLocalTool(context.Background(), claimed, ports.Workspace{WorktreePath: root}, "branch", codex.ToolCallRequest{
		Namespace: stringPointer("git"), Tool: "status",
	})
	require.NoError(t, err)
	require.Contains(t, statusResult.ContentItems[0].Text, "file.go")
	_, err = processor.executeLocalTool(context.Background(), claimed, ports.Workspace{}, "branch", codex.ToolCallRequest{Namespace: stringPointer("git"), Tool: "unknown"})
	require.Error(t, err)
}

func TestRemoteDiscordEventReporterForwardsTimeline(t *testing.T) {
	type reportedEvent struct {
		eventType string
		payload   json.RawMessage
	}
	var events []reportedEvent
	reporter := remoteDiscordEventReporter(func(eventType string, payload json.RawMessage) {
		events = append(events, reportedEvent{eventType: eventType, payload: payload})
	})
	item := json.RawMessage(`{"item":{"id":"commentary-1","type":"agentMessage","phase":"commentary"}}`)
	reporter("item/started", item)
	reporter("turn/started", json.RawMessage(`{"turn":{"id":"turn-1"}}`))
	require.Len(t, events, 3)
	require.Equal(t, "item/started", events[0].eventType)
	require.JSONEq(t, string(item), string(events[0].payload))
	require.Equal(t, "turn/started", events[1].eventType)
	require.Equal(t, "discord.progress", events[2].eventType)
	require.JSONEq(t, `{"detail":"Codex 正在处理当前消息。","state":"running"}`,
		string(events[2].payload))
}

func TestLocalToolCallAuditIsIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	mock.MatchExpectationsInOrder(false)
	mock.ExpectClose()
	t.Cleanup(func() {
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	processor := &Processor{db: db}
	runID := uuid.New()
	intentID := uuid.New()
	callRecordID := uuid.New()
	claimed := &codexcontrol.ClaimedControl{RunID: runID, Intent: codexcontrol.Intent{ID: intentID}}
	request := codex.ToolCallRequest{
		ThreadID: "thread-1", TurnID: "turn-1", CallID: "call-1",
		Namespace: stringPointer("git"), Tool: "status", Arguments: json.RawMessage(`{}`),
	}
	missingNamespace := request
	missingNamespace.Namespace = nil
	_, err = processor.auditLocalToolCall(context.Background(), claimed, missingNamespace, nil)
	require.ErrorContains(t, err, "缺少 namespace")
	insertPattern := regexp.QuoteMeta("INSERT INTO tool_calls")
	mock.ExpectQuery(insertPattern).
		WithArgs(runID, intentID, request.ThreadID, request.TurnID, request.CallID, "git", request.Tool, request.Arguments).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(callRecordID))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE tool_calls SET status = 'completed'")).
		WithArgs(callRecordID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	executions := 0
	execute := func() (codex.ToolCallResult, error) {
		executions++
		return codex.TextToolResult("clean", true), nil
	}
	result, err := processor.auditLocalToolCall(context.Background(), claimed, request, execute)
	require.NoError(t, err)
	require.True(t, result.Success)

	storedJSON, err := json.Marshal(result)
	require.NoError(t, err)
	mock.ExpectQuery(insertPattern).
		WithArgs(runID, intentID, request.ThreadID, request.TurnID, request.CallID, "git", request.Tool, request.Arguments).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status, result, error FROM tool_calls")).
		WithArgs(request.ThreadID, request.TurnID, request.CallID, "git", request.Tool, string(request.Arguments)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "result", "error"}).AddRow("completed", storedJSON, nil))

	replayed, err := processor.auditLocalToolCall(context.Background(), claimed, request, execute)
	require.NoError(t, err)
	require.Equal(t, result, replayed)
	require.Equal(t, 1, executions)

	conflicting := request
	conflicting.Tool = "commit"
	conflicting.Arguments = json.RawMessage(`{"message":"conflict"}`)
	mock.ExpectQuery(insertPattern).
		WithArgs(runID, intentID, conflicting.ThreadID, conflicting.TurnID, conflicting.CallID, "git", conflicting.Tool, conflicting.Arguments).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status, result, error FROM tool_calls")).
		WithArgs(conflicting.ThreadID, conflicting.TurnID, conflicting.CallID, "git", conflicting.Tool, string(conflicting.Arguments)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "result", "error"}))
	_, err = processor.auditLocalToolCall(context.Background(), claimed, conflicting, execute)
	require.ErrorContains(t, err, "与既有请求不一致")
	require.Equal(t, 1, executions)

	require.Equal(t, 1, executions)
}

func TestDiscordStopUsesCanceledProjection(t *testing.T) {
	state, detail := discordFailureProjection(context.Background(), nil, uuid.Nil, errDiscordTurnStopped)
	require.Equal(t, discordintegration.ConversationCanceled, state)
	require.Contains(t, detail, "主动停止")
	require.False(t, needsCleanupInterrupt(errDiscordTurnStopped))
	require.False(t, needsCleanupInterrupt(fmt.Errorf("包装停止错误: %w", errDiscordTurnStopped)))
	require.True(t, needsCleanupInterrupt(errors.New("stdio 中断")))

	state, detail = discordFailureProjection(context.Background(), nil, uuid.Nil, errors.New("runtime failed"))
	require.Equal(t, discordintegration.ConversationFailed, state)
	require.Contains(t, detail, "后台已记录")
}

func TestDiscordStopSurvivesHeartbeatCancellationRace(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	jobID := uuid.New()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status, COALESCE(last_error_code, '')")).
		WithArgs(jobID).
		WillReturnRows(sqlmock.NewRows([]string{"status", "last_error_code"}).AddRow("canceled", "user_interrupt"))
	require.True(t, discordStopRequested(context.Background(), db, jobID, context.Canceled))
	mock.ExpectClose()
}

type fakeWorkspace struct {
	status string
	err    error
}

func (w *fakeWorkspace) Ensure(context.Context, ports.WorkspaceSpec, string) (ports.Workspace, error) {
	return ports.Workspace{}, errors.New("not implemented")
}
func (w *fakeWorkspace) Status(context.Context, string) (string, error) { return w.status, w.err }
func (w *fakeWorkspace) Commit(context.Context, string, string) (string, error) {
	return "commit-sha", w.err
}
func (w *fakeWorkspace) Publish(context.Context, string, string, string) (string, error) {
	return "", errors.New("not implemented")
}
func (w *fakeWorkspace) Remove(context.Context, string, string) error { return nil }

func stringPointer(value string) *string { return &value }
