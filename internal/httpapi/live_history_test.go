package httpapi

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBuildLiveHistoryKeepsNewestMessagesInChronologicalOrder(t *testing.T) {
	rows := []liveHistoryRow{
		{Role: "assistant", Text: "newest"},
		{Role: "user", Text: "middle"},
		{Role: "developer", Text: "oldest"},
		{Role: "system", Text: "must be ignored"},
	}
	history := buildLiveHistory(rows)
	require.Len(t, history, 3)
	require.Equal(t, "oldest", history[0].Content[0].Text)
	require.Equal(t, "middle", history[1].Content[0].Text)
	require.Equal(t, "newest", history[2].Content[0].Text)
	require.Equal(t, "output_text", history[2].Content[0].Type)
}

func TestBuildLiveHistoryDropsOldestBeforeNewest(t *testing.T) {
	rows := []liveHistoryRow{
		{Role: "user", Text: strings.Repeat("newest ", 500)},
		{Role: "assistant", Text: strings.Repeat("middle ", 500)},
		{Role: "user", Text: strings.Repeat("oldest ", 500)},
	}
	history := buildLiveHistory(rows)
	require.Len(t, history, 2)
	require.Contains(t, history[0].Content[0].Text, "middle")
	require.Contains(t, history[1].Content[0].Text, "newest")
}

func TestBuildLiveHistoryTruncatesOneOversizedNewestMessage(t *testing.T) {
	history := buildLiveHistory([]liveHistoryRow{{Role: "user", Text: strings.Repeat("x", liveHistoryBudget*2)}})
	require.Len(t, history, 1)
	require.LessOrEqual(t, len([]rune(history[0].Content[0].Text))+4, liveHistoryBudget)
}

func TestLiveTranscriptHelpersReadNestedContent(t *testing.T) {
	sessionID := uuid.New()
	event := map[string]any{
		"type": "output_transcript.added",
		"item": map[string]any{
			"id":      "item-1",
			"content": []any{map[string]any{"text": "hello"}},
		},
	}
	require.Equal(t, "open", transcriptIdentity("output_transcript.added", event))
	require.Equal(t, "hello", eventText(event))
	require.Equal(t, sessionID.String()+":assistant:open", transcriptKey(sessionID, "assistant", "output_transcript.added", event))
}

func TestLiveTranscriptDoneKeepsItemIdentity(t *testing.T) {
	sessionID := uuid.New()
	event := map[string]any{"type": "output_transcript.done", "response_id": "response-1", "text": "hello"}
	require.Equal(t, "response-1", transcriptIdentity("output_transcript.done", event))
	require.Equal(t, sessionID.String()+":assistant:response-1", transcriptKey(sessionID, "assistant", "output_transcript.done", event))
}

func TestLiveTranscriptAddedEventsShareOpenIdentity(t *testing.T) {
	sessionID := uuid.New()
	first := map[string]any{"type": "input_transcript.added", "item": map[string]any{"id": "item-1", "text": "这是"}}
	second := map[string]any{"type": "input_transcript.added", "item": map[string]any{"id": "item-2", "text": "泰"}}
	require.Equal(t, sessionID.String()+":user:open", transcriptKey(sessionID, "user", "input_transcript.added", first))
	require.Equal(t, transcriptKey(sessionID, "user", "input_transcript.added", first), transcriptKey(sessionID, "user", "input_transcript.added", second))
}

func TestLiveTranscriptHelpersReadNestedDelta(t *testing.T) {
	event := map[string]any{
		"item": map[string]any{"content": []any{map[string]any{"delta": "hello "}}},
	}
	require.Equal(t, "hello ", eventDelta(event))
}
