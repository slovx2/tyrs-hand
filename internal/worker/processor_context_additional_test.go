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
