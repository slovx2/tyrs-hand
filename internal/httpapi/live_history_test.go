package httpapi

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/live"
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
		"response": map[string]any{
			"id": "response-1",
			"output": []any{map[string]any{
				"content": []any{map[string]any{"text": "hello"}},
			}},
		},
	}
	require.Equal(t, "response-1", transcriptIdentity(event))
	require.Equal(t, "hello", eventText(event))
	require.Equal(t, sessionID.String()+":assistant:response-1", transcriptKey(sessionID, "assistant", "output_transcript.added", event))
}

func TestLiveTranscriptHelpersReadNestedDelta(t *testing.T) {
	event := map[string]any{
		"content": []any{map[string]any{"part": map[string]any{"delta": "hello "}}},
	}
	require.Equal(t, "hello ", eventDelta(event))
}

func TestLiveHistoryAppendEventUsesContextAppend(t *testing.T) {
	sessionID := uuid.New()
	event := liveHistoryAppendEvent(sessionID, 1, live.InputMessage{
		Type: "message", Role: "assistant",
		Content: []live.InputContent{{Type: "output_text", Text: "hi"}},
	})
	require.Equal(t, "session.context.append", event["type"])
	require.Equal(t, "assistant", event["role"])
	require.Equal(t, "speakable", event["channel"])
	require.Equal(t, "recover-"+sessionID.String()+"-1", event["event_id"])
	content := event["content"].([]map[string]string)
	require.Equal(t, "output_text", content[0]["type"])
	require.Equal(t, "hi", content[0]["text"])
}
