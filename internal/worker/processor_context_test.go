package worker

import (
	"encoding/json"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

func TestFinalAnswerFromEventAcceptsPlan(t *testing.T) {
	event := codex.Event{Method: "item/completed", Params: json.RawMessage(
		`{"item":{"type":"plan","text":"  plan body  "}}`)}
	require.Equal(t, "plan body", finalAnswerFromEvent(event))
	answer, outputType := finalOutputFromEvent(event)
	require.Equal(t, "plan body", answer)
	require.Equal(t, "plan", outputType)

	event.Params = json.RawMessage(
		`{"item":{"type":"agentMessage","phase":"final_answer","text":"answer"}}`)
	require.Equal(t, "answer", finalAnswerFromEvent(event))
	_, outputType = finalOutputFromEvent(event)
	require.Equal(t, "agentMessage", outputType)

	event.Params = json.RawMessage(
		`{"item":{"type":"agentMessage","phase":"commentary","text":"progress"}}`)
	require.Empty(t, finalAnswerFromEvent(event))
}
