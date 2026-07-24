package worker

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/stretchr/testify/require"
)

type recordingThreadRuntimeClient struct {
	methods   []string
	resumeErr error
}

func (c *recordingThreadRuntimeClient) Call(_ context.Context, method string, _ any, _ any) error {
	c.methods = append(c.methods, method)
	if method == "thread/resume" {
		return c.resumeErr
	}
	return errors.New("不应调用 " + method)
}

func TestGitHubWorkItemAdditionalContextIncludesIssueAndPullRequestMetadata(t *testing.T) {
	workspace := ports.Workspace{Branch: "tyrs-hand/pull-8", HeadSHA: "workspace-head"}
	pull := jobContext{
		Owner: "owner", Repository: "repo", Kind: "pull_request", Number: 8,
		HeadSHA: "abc123", HeadRef: "feature", HeadRepository: "fork/repo",
		BaseSHA: "def456", BaseRef: "main", HTMLURL: "https://github.com/owner/repo/pull/8",
	}
	entry := githubWorkItemAdditionalContext(pull, workspace)["github_work_item"]
	require.Equal(t, "application", entry.Kind)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(entry.Value), &payload))
	require.Equal(t, "owner/repo", payload["repository"])
	require.Equal(t, "https://github.com/owner/repo/pull/8", payload["url"])
	pullRequest := payload["pullRequest"].(map[string]any)
	require.Equal(t, "fork/repo", pullRequest["sourceRepository"])
	require.Equal(t, "feature", pullRequest["sourceBranch"])
	require.Equal(t, "refs/remotes/pull/8", pullRequest["fetchedRef"])

	issue := jobContext{Owner: "owner", Repository: "repo", Kind: "issue", Number: 9}
	entry = githubWorkItemAdditionalContext(issue, workspace)["github_work_item"]
	payload = make(map[string]any)
	require.NoError(t, json.Unmarshal([]byte(entry.Value), &payload))
	require.Equal(t, "https://github.com/owner/repo/issues/9", payload["url"])
	require.NotContains(t, payload, "pullRequest")
}

func TestWorkerThreadOptionsForceUnattendedCommandPolicy(t *testing.T) {
	tests := []struct {
		name     string
		sandbox  string
		approval string
	}{
		{name: "本地 GitHub Profile 旧值", sandbox: "workspace-write", approval: "on-request"},
		{name: "远程 GitHub 快照旧值", sandbox: "read-only", approval: "untrusted"},
		{name: "本地 Discord 空值"},
		{name: "远程 Discord 已是目标值", sandbox: "danger-full-access", approval: "never"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := ports.ThreadOptions{
				CWD: "/workspace", Model: "gpt-test", Sandbox: test.sandbox,
				ApprovalPolicy: test.approval, NetworkEnabled: true,
			}

			actual := workerThreadOptions(input)

			require.Equal(t, workerCodexSandbox, actual.Sandbox)
			require.Equal(t, workerCodexApprovalPolicy, actual.ApprovalPolicy)
			require.Equal(t, input.CWD, actual.CWD)
			require.Equal(t, input.Model, actual.Model)
			require.Equal(t, input.NetworkEnabled, actual.NetworkEnabled)
		})
	}
}

func TestDiscordThreadResumesExistingThread(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	claimed := &codexcontrol.ClaimedControl{
		Intent: codexcontrol.Intent{ID: uuid.New(), ControlID: uuid.New(),
			SourceType: codexcontrol.SourceDiscord},
		ExternalThreadID: "discord-thread", CodexHomeKey: "shared-home",
		Recovering: true,
		LeaseToken: "lease", LeaseEpoch: 4,
	}
	mock.ExpectExec("UPDATE codex_thread_controls SET").
		WithArgs(claimed.ControlID, sqlmock.AnyArg(), claimed.LeaseEpoch,
			"discord-thread", "shared-home").
		WillReturnResult(sqlmock.NewResult(0, 1))
	client := &recordingThreadRuntimeClient{}
	processor := &Processor{controls: codexcontrol.NewRepository(db, time.Minute)}

	threadID, err := processor.ensureThread(context.Background(), codex.NewRuntime(client),
		claimed, ports.ThreadOptions{}, "shared-home")
	require.NoError(t, err)
	require.Equal(t, "discord-thread", threadID)
	require.Equal(t, []string{"thread/resume"}, client.methods)
}

func TestGitHubThreadResumesExistingThread(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	claimed := &codexcontrol.ClaimedControl{
		Intent: codexcontrol.Intent{ID: uuid.New(), ControlID: uuid.New(),
			SourceType: codexcontrol.SourceGitHub},
		ExternalThreadID: "github-thread", CodexHomeKey: "shared-home",
		Recovering: true,
		LeaseToken: "lease", LeaseEpoch: 5,
	}
	mock.ExpectExec("UPDATE codex_thread_controls SET").
		WithArgs(claimed.ControlID, sqlmock.AnyArg(), claimed.LeaseEpoch,
			"github-thread", "shared-home").
		WillReturnResult(sqlmock.NewResult(0, 1))
	client := &recordingThreadRuntimeClient{}
	processor := &Processor{controls: codexcontrol.NewRepository(db, time.Minute)}

	threadID, err := processor.ensureThread(context.Background(), codex.NewRuntime(client),
		claimed, ports.ThreadOptions{}, "shared-home")
	require.NoError(t, err)
	require.Equal(t, "github-thread", threadID)
	require.Equal(t, []string{"thread/resume"}, client.methods)
}

func TestDiscordResumeFailureDoesNotStartReplacementThread(t *testing.T) {
	claimed := &codexcontrol.ClaimedControl{
		Intent:           codexcontrol.Intent{SourceType: codexcontrol.SourceDiscord},
		ExternalThreadID: "missing-thread", CodexHomeKey: "shared-home",
		Recovering: true,
	}
	client := &recordingThreadRuntimeClient{resumeErr: errors.New("thread missing")}

	_, err := (&Processor{}).ensureThread(context.Background(), codex.NewRuntime(client),
		claimed, ports.ThreadOptions{}, "shared-home")
	require.ErrorContains(t, err, "恢复 Codex Thread")
	require.Equal(t, []string{"thread/resume"}, client.methods)
}

func TestPersistentCodexHomeUsesControlPathOrStableIdentity(t *testing.T) {
	claimed := &codexcontrol.ClaimedControl{Intent: codexcontrol.Intent{
		RepositoryID: uuid.New(), AgentProfileID: uuid.New(),
	}}
	expected := filepath.Join("/codex", "pools", claimed.RepositoryID.String(),
		claimed.AgentProfileID.String())
	require.Equal(t, expected, persistentCodexHome("/codex", claimed))

	claimed.CodexHomeKey = "/codex/pools/legacy-provider-home"
	require.Equal(t, claimed.CodexHomeKey, persistentCodexHome("/codex", claimed))
}
