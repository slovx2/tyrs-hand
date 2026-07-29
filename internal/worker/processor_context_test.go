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

	event.Params = json.RawMessage(
		`{"item":{"type":"agentMessage","phase":"final_answer","text":"answer"}}`)
	require.Equal(t, "answer", finalAnswerFromEvent(event))

	event.Params = json.RawMessage(
		`{"item":{"type":"agentMessage","phase":"commentary","text":"progress"}}`)
	require.Empty(t, finalAnswerFromEvent(event))
}
