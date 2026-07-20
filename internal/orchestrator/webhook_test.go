package orchestrator

import (
	"database/sql"
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
	require.True(t, mentions("(@tyrs-hand), inspect", "tyrs-hand"))
	require.False(t, mentions("no mention", "tyrs-hand"))
	require.False(t, mentions("@tyrs-hand-suffix inspect", "tyrs-hand"))
	require.False(t, mentions("prefix@tyrs-hand.example", "tyrs-hand"))
	require.False(t, mentions("`@tyrs-hand` is an example", "tyrs-hand"))
	require.False(t, mentions("``use `code` and @tyrs-hand here``", "tyrs-hand"))
	require.False(t, mentions("before\n```text\n@tyrs-hand inspect\n```\nafter", "tyrs-hand"))
	require.False(t, mentions("before\n~~~\n@tyrs-hand inspect\n~~~\nafter", "tyrs-hand"))
	require.False(t, mentions("\\@tyrs-hand is escaped", "tyrs-hand"))
	require.False(t, mentions("https://github.com/@tyrs-hand", "tyrs-hand"))
	require.True(t, mentions("`example` then @tyrs-hand inspect", "tyrs-hand"))
	require.True(t, mentions("> @tyrs-hand inspect", "tyrs-hand"))
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

func TestSlashCommandArguments(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected string
		matched  bool
	}{
		{name: "arguments", body: "/tyrs-hand inspect this", expected: "inspect this", matched: true},
		{name: "multiline", body: "/tyrs-hand inspect\nmore context", expected: "inspect\nmore context", matched: true},
		{name: "command only", body: "/tyrs-hand", expected: "", matched: true},
		{name: "surrounding whitespace", body: "  /tyrs-hand\tinspect  ", expected: "inspect", matched: true},
		{name: "second line", body: "explanation\n/tyrs-hand inspect", matched: false},
		{name: "suffix", body: "/tyrs-hand-extra inspect", matched: false},
		{name: "case sensitive", body: "/Tyrs-Hand inspect", matched: false},
		{name: "code fence", body: "```\n/tyrs-hand inspect\n```", matched: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, matched := slashCommandArguments(test.body, "tyrs-hand")
			require.Equal(t, test.matched, matched)
			require.Equal(t, test.expected, actual)
		})
	}
}

func TestFirstLineMentions(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		matched bool
	}{
		{name: "leading mention", body: "@tyrshand inspect this", matched: true},
		{name: "natural language", body: "请 @tyrshand 帮我检查", matched: true},
		{name: "case insensitive", body: "请 @TyrsHand 检查", matched: true},
		{name: "visible after inline code", body: "`example` then @tyrshand inspect", matched: true},
		{name: "windows first line", body: "@tyrshand inspect\r\ncontext", matched: true},
		{name: "second line", body: "context\n@tyrshand inspect"},
		{name: "quoted", body: "> @tyrshand inspect"},
		{name: "indented quote", body: "  > @tyrshand inspect"},
		{name: "inline code", body: "`@tyrshand` inspect"},
		{name: "fenced code", body: "```text @tyrshand"},
		{name: "indented code", body: "    @tyrshand inspect"},
		{name: "tab indented code", body: "\t@tyrshand inspect"},
		{name: "mixed tab indented code", body: " \t@tyrshand inspect"},
		{name: "escaped", body: "\\@tyrshand inspect"},
		{name: "url", body: "https://github.com/@tyrshand inspect"},
		{name: "suffix", body: "@tyrshand-extra inspect"},
		{name: "email style prefix", body: "hello@tyrshand.example"},
		{name: "empty bot login", body: "@tyrshand inspect"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			botLogin := "tyrshand"
			if test.name == "empty bot login" {
				botLogin = ""
			}
			require.Equal(t, test.matched, firstLineMentions(test.body, botLogin))
		})
	}
}

func TestMatchTrigger(t *testing.T) {
	event := domain.NormalizedEvent{EventName: "issue_comment", Action: "created", Body: "/tyrs-hand inspect\ncontext"}
	command := triggerRule{TriggerKind: "slash_command", TriggerValue: sql.NullString{String: "tyrs-hand", Valid: true}}
	matched, ok := matchTrigger(command, event, "tyrs-hand")
	require.True(t, ok)
	require.Equal(t, "inspect\ncontext", matched.Body)
	require.Equal(t, "comment_first_line", matched.Evidence["source"])

	event.Body = "请 @tyrshand 帮我检查\n更多上下文"
	mentionCommand := triggerRule{TriggerKind: "mention_command"}
	matched, ok = matchTrigger(mentionCommand, event, "tyrshand")
	require.True(t, ok)
	require.Equal(t, event.Body, matched.Body)
	require.Equal(t, "comment_first_line_mention", matched.Evidence["source"])

	event.Label = "TYRS-HAND"
	label := triggerRule{TriggerKind: "label", TriggerValue: sql.NullString{String: "tyrs-hand", Valid: true}}
	_, ok = matchTrigger(label, event, "tyrs-hand")
	require.True(t, ok)

	event.Body = "please ask @tyrs-hand to inspect"
	legacy := triggerRule{TriggerKind: "legacy_mention"}
	_, ok = matchTrigger(legacy, event, "tyrs-hand")
	require.True(t, ok)

	_, ok = matchTrigger(triggerRule{TriggerKind: "unknown"}, event, "tyrs-hand")
	require.False(t, ok)
}

func TestDiscordPermissionSyncForEvent(t *testing.T) {
	event := domain.NormalizedEvent{
		EventName: "installation_repositories", InstallationID: 42, RepositoryID: 100,
		Installation: domain.SCMInstallationEvent{
			Repositories:         []domain.SCMRepository{{ExternalID: 101}, {ExternalID: 100}},
			RemovedRepositoryIDs: []int64{102, 101},
		},
	}
	request := discordPermissionSyncForEvent(event)
	require.NotNil(t, request)
	require.Equal(t, int64(42), request.InstallationID)
	require.Equal(t, []int64{100, 101, 102}, request.RepositoryIDs)

	event.EventName = "membership"
	event.RepositoryID = 0
	event.Installation.Repositories = nil
	event.Installation.RemovedRepositoryIDs = nil
	request = discordPermissionSyncForEvent(event)
	require.NotNil(t, request)
	require.Empty(t, request.RepositoryIDs)

	event.EventName = "issue_comment"
	require.Nil(t, discordPermissionSyncForEvent(event))
}
