package tools

import (
	"encoding/json"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

func TestToolArgumentBoundaries(t *testing.T) {
	auth := authorization{Owner: "Owner", Repository: "Repo", Number: 1, AllowedNumbers: []int{1, 8}}
	require.NoError(t, validateArguments(json.RawMessage(`{"owner":"owner","repo":"repo","issueNumber":1}`), auth))
	require.NoError(t, validateArguments(json.RawMessage(`{"owner":"Owner","repo":"Repo","pullNumber":8}`), auth))
	require.NoError(t, validateArguments(json.RawMessage(`{"owner":"Owner","repo":"Repo","issue_number":"8"}`), auth))
	require.Error(t, validateArguments(json.RawMessage(`{"owner":"other"}`), auth))
	require.Error(t, validateArguments(json.RawMessage(`{"repo":"other"}`), auth))
	require.Error(t, validateArguments(json.RawMessage(`{"pullNumber":9}`), auth))
	require.Error(t, validateArguments(json.RawMessage(`{"pull_request_number":"invalid"}`), auth))
	require.Error(t, validateArguments(json.RawMessage(`{"issue_number":1.5}`), auth))
	require.Error(t, validateArguments(json.RawMessage(`{"issue_number":true}`), auth))
	require.Error(t, validateArguments(json.RawMessage(`[]`), auth))
	require.True(t, contains([]string{"a", "b"}, "b"))
	require.False(t, contains([]string{"a", "b"}, "c"))
	require.True(t, containsNumber([]int{1, 2}, 2))
	require.False(t, containsNumber([]int{1, 2}, 3))
	number, valid := argumentNumber("42")
	require.True(t, valid)
	require.Equal(t, 42, number)
	_, valid = argumentNumber(struct{}{})
	require.False(t, valid)
}

func TestPullRequestNumberExtraction(t *testing.T) {
	require.Equal(t, 42, pullRequestNumber(codex.TextToolResult(`{"pull_request":{"number":42}}`, true)))
	require.Equal(t, 19, pullRequestNumber(codex.TextToolResult("created https://github.com/o/r/pull/19", true)))
	require.Zero(t, pullRequestNumber(codex.TextToolResult("created", true)))
	require.Equal(t, 7, findNumber([]any{map[string]any{"number": float64(7)}}))
	require.Equal(t, 8, findNumber(map[string]any{"nested": map[string]any{"number": float64(8)}}))
	require.Zero(t, findNumber("none"))
}
