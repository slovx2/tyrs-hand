package orchestrator

import (
	"testing"

	"github.com/slovx2/tyrs-hand/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestRuleHelpers(t *testing.T) {
	event := domain.NormalizedEvent{
		Owner: "owner", Repository: "repo", Number: 42, Actor: "alice",
		Body: "please fix", EventName: "issue_comment", Action: "created",
	}
	rendered := renderInstruction("{{owner}}/{{repository}}#{{number}} {{actor}} {{event}} {{action}} {{body}}", event)
	require.Equal(t, "owner/repo#42 alice issue_comment created please fix", rendered)
	require.True(t, mentions("Could @TYRS-HAND inspect?", "tyrs-hand"))
	require.False(t, mentions("no mention", "tyrs-hand"))
	require.False(t, mentions("@tyrs-hand", ""))
	require.True(t, isBot("tyrs-hand[bot]", "tyrs-hand"))
	require.True(t, isBot("TYRS-HAND", "tyrs-hand"))
	require.False(t, isBot("alice", "tyrs-hand"))
	require.False(t, isBot("", ""))
	require.Greater(t, permissionRank("admin"), permissionRank("write"))
	require.Greater(t, permissionRank("maintain"), permissionRank("push"))
	require.Greater(t, permissionRank("write"), permissionRank("triage"))
	require.Greater(t, permissionRank("triage"), permissionRank("read"))
	require.Equal(t, permissionRank("pull"), permissionRank("read"))
	require.Equal(t, 0, permissionRank("none"))
	require.JSONEq(t, `["a","b"]`, string(encode([]string{"a", "b"})))
}
