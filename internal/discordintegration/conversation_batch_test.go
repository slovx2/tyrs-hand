package discordintegration

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestDiscussionInstructionKeepsChronologyAndEscapesContent(t *testing.T) {
	start := time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
	aliceID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	bobID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	instruction := discussionInstruction([]pendingDiscussionMessage{
		{ID: "1", ParticipantID: aliceID, DisplayName: "Alice",
			Body: "先讨论 <方案>", ReceivedAt: start},
		{ID: "2", ParticipantID: bobID, DisplayName: "Bob",
			Body: "再 @ Codex & 执行", ReceivedAt: start.Add(time.Second)},
	})
	require.Less(t, len("先讨论"), len(instruction))
	require.Less(t, indexOf(t, instruction, "Alice"), indexOf(t, instruction, "Bob"))
	require.Contains(t, instruction, "&lt;方案&gt;")
	require.Contains(t, instruction, "&amp; 执行")
	require.Contains(t, instruction, `participant_id="`+aliceID.String()+`"`)
	require.Contains(t, instruction, `participant_id="`+bobID.String()+`"`)

	require.Equal(t, "保持原始正文", discussionInstruction([]pendingDiscussionMessage{{
		ID: "3", Body: "保持原始正文", ReceivedAt: start,
	}}))
}

func indexOf(t *testing.T, value, needle string) int {
	t.Helper()
	index := -1
	for position := 0; position+len(needle) <= len(value); position++ {
		if value[position:position+len(needle)] == needle {
			index = position
			break
		}
	}
	require.NotEqual(t, -1, index)
	return index
}
