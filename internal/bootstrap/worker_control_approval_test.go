//go:build integration

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/interactiveprotocol"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

type controlApprovalCase struct {
	tool, method, decision string
	input                  map[string]any
	path, id               string
	sent, resultSeen       bool
}

type controlApprovalScenario struct {
	mu     sync.Mutex
	active *controlApprovalCase
}

func newControlApprovalScenario() *controlApprovalScenario { return &controlApprovalScenario{} }

func (s *controlApprovalScenario) respond(t *testing.T, w http.ResponseWriter, body []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.active
	if active == nil {
		return false
	}
	var payload struct {
		Tools    []struct{ Name string }
		Messages []struct {
			Content json.RawMessage
		}
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Error(err)
		return false
	}
	if !strings.Contains(string(body), active.id) {
		return false
	}
	if !active.sent {
		found := false
		for _, tool := range payload.Tools {
			found = found || tool.Name == active.tool
		}
		if !found {
			t.Errorf("真实 SDK 未声明 %s 工具", active.tool)
		}
		active.sent = true
		input, _ := json.Marshal(active.input)
		bootstrapClaudeStart(w)
		bootstrapEvent(w, "content_block_start", map[string]any{"index": 0, "content_block": map[string]any{
			"type": "tool_use", "id": active.id, "name": active.tool, "input": map[string]any{}}})
		bootstrapEvent(w, "content_block_delta", map[string]any{"index": 0, "delta": map[string]any{
			"type": "input_json_delta", "partial_json": string(input)}})
		bootstrapClaudeEnd(w, "tool_use")
		return true
	}
	for _, message := range payload.Messages {
		var blocks []struct {
			Type      string `json:"type"`
			ToolUseID string `json:"tool_use_id"`
			IsError   bool   `json:"is_error"`
		}
		if json.Unmarshal(message.Content, &blocks) == nil {
			for _, block := range blocks {
				if block.Type == "tool_result" && block.ToolUseID == active.id {
					active.resultSeen = true
					if block.IsError != (active.decision == "decline") {
						t.Errorf("%s 工具执行结果与实际审批不一致", active.id)
					}
				}
			}
		}
	}
	bootstrapModelText(w, true)
	return true
}

func (s *controlApprovalScenario) run(t *testing.T, ctx context.Context, app *WorkerApp, f controlRuntimeFixture, thread string, discord *controlDiscordFixture) {
	t.Helper()
	entry, err := app.Runtimes.Entry(runtimeidentity.Claude)
	require.NoError(t, err)
	received := make(chan codex.ServerRequest, 8)
	client, _ := connectBootstrapSSHWithOptions(t, ctx, entry, f.signer, codex.SocketClientOptions{
		ServerRequestHandler: func(ctx context.Context, request codex.ServerRequest) (any, error) {
			received <- request
			<-ctx.Done()
			return nil, ctx.Err()
		}})
	require.NoError(t, client.Call(ctx, "thread/resume", map[string]any{"threadId": thread}, nil))
	events := client.Subscribe(codex.ThreadFilter{ThreadID: thread})
	t.Cleanup(events.Close)
	manager := discordintegration.NewManager(f.db, nil)
	var evidence []map[string]any
	for index, test := range []struct{ tool, method, decision string }{
		{"Bash", interactiveprotocol.CommandApproval, "accept"},
		{"Write", interactiveprotocol.FileApproval, "accept"},
		{"Write", interactiveprotocol.FileApproval, "decline"},
	} {
		id := fmt.Sprintf("toolu_discord_approval_%d", index)
		path := filepath.Join(f.cfg.WorkerWorkspaceRoot, id+".txt")
		input := map[string]any{"file_path": path, "content": id}
		if test.tool == "Bash" {
			input = map[string]any{"command": "printf " + id + " >> " + path}
		}
		active := &controlApprovalCase{tool: test.tool, method: test.method, decision: test.decision, path: path, id: id, input: input}
		s.mu.Lock()
		s.active = active
		s.mu.Unlock()
		require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": thread,
			"approvalPolicy": "on-request", "sandboxPolicy": map[string]any{"type": "dangerFullAccess"},
			"input": []map[string]any{{"type": "text", "text": id, "text_elements": []any{}}}}, nil))
		var request codex.ServerRequest
		select {
		case request = <-received:
		case <-time.After(15 * time.Second):
			t.Fatal("Desktop 未收到原生审批")
		}
		require.Equal(t, test.method, request.Method)
		require.NoFileExists(t, path, "用户回答前不能产生副作用")
		var requestID uuid.UUID
		discord.deliverUntil(t, ctx, func() bool {
			err := f.db.QueryRowContext(ctx, `SELECT q.id FROM codex_interactive_requests q
				JOIN codex_thread_controls c ON c.id=q.control_id WHERE c.engine='claude-code'
				AND q.thread_id=$1 AND q.app_server_request_id=$2::jsonb AND q.status='pending'
				AND q.discord_message_id IS NOT NULL`, thread, request.ID).Scan(&requestID)
			return err == nil
		})
		option := 0
		if test.decision == "decline" {
			option = 1
		}
		answer, err := manager.AnswerInteractive(ctx, "protocol", requestID, 0, option, "")
		require.NoError(t, err)
		require.True(t, answer.Complete)
		require.Contains(t, answer.Card.Header, "Claude Code")
		require.NoError(t, discordintegration.ProjectInteractiveRequest(ctx, f.db, requestID))
		awaitBootstrapTurn(t, ctx, events)
		awaitControlRunCount(t, ctx, f.db, runtimeidentity.Claude, 3+index)
		if test.decision == "accept" {
			content, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, id, string(content), "批准工具只执行一次")
		} else {
			require.NoFileExists(t, path)
		}
		s.mu.Lock()
		seen := active.resultSeen
		s.mu.Unlock()
		require.True(t, seen, "真实工具结果必须回到模型上下文")
		evidence = append(evidence, map[string]any{"method": test.method, "decision": test.decision,
			"requestId": request.ID, "controlRequestId": requestID, "modelReceivedToolResult": seen,
			"fileCreated": test.decision == "accept"})
	}
	s.mu.Lock()
	s.active = nil
	s.mu.Unlock()
	saveBootstrapArtifact(t, "effects", runtimeidentity.Claude, map[string]any{"nativeApprovals": evidence})
	discord.mu.Lock()
	saveBootstrapArtifact(t, "discord", runtimeidentity.Claude, map[string]any{"deliveries": discord.deliveries})
	discord.mu.Unlock()
}
