package worker

import (
	"encoding/json"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/stretchr/testify/require"
)

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
