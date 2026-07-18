package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/queue"
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
	require.True(t, eventMatchesTurn(started, threadID, turnID))
	require.False(t, eventMatchesTurn(json.RawMessage(`{"broken":`), threadID, turnID))
	matched, status := completedTurn(json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}`), threadID, turnID)
	require.True(t, matched)
	require.Equal(t, "completed", status)
	matched, _ = completedTurn(started, "other", turnID)
	require.False(t, matched)

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
	require.Len(t, spec.Tools, 2)
	require.Len(t, shortID(uuid.MustParse("12345678-1234-1234-1234-123456789012")), 8)

	workspace := &fakeWorkspace{status: "## main\n M file.go"}
	processor := &Processor{workspace: workspace}
	claimed := &queue.ClaimedJob{}
	statusResult, err := processor.handleTool(context.Background(), claimed, ports.Workspace{WorktreePath: root}, "branch", codex.ToolCallRequest{
		Namespace: stringPointer("git"), Tool: "status",
	})
	require.NoError(t, err)
	require.Contains(t, statusResult.ContentItems[0].Text, "file.go")
	_, err = processor.handleTool(context.Background(), claimed, ports.Workspace{}, "branch", codex.ToolCallRequest{Tool: "status"})
	require.Error(t, err)
	_, err = processor.handleTool(context.Background(), claimed, ports.Workspace{}, "branch", codex.ToolCallRequest{Namespace: stringPointer("git"), Tool: "unknown"})
	require.Error(t, err)
}

type fakeWorkspace struct {
	status string
	err    error
}

func (w *fakeWorkspace) Ensure(context.Context, ports.WorkspaceSpec, string) (ports.Workspace, error) {
	return ports.Workspace{}, errors.New("not implemented")
}
func (w *fakeWorkspace) Status(context.Context, string) (string, error) { return w.status, w.err }
func (w *fakeWorkspace) Publish(context.Context, string, string, string) (string, error) {
	return "", errors.New("not implemented")
}
func (w *fakeWorkspace) Remove(context.Context, string, string) error { return nil }

func stringPointer(value string) *string { return &value }
