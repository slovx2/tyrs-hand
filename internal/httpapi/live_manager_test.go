package httpapi

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestLivePayloadRemovesAudioData(t *testing.T) {
	payload := sanitizeLivePayload(map[string]any{
		"type": "session.output_audio.delta", "delta": "secret-audio", "audio": "also-secret",
	}, true)
	encoded := eventDedupeKey(map[string]any{"type": "session.output_audio.delta", "delta": "secret-audio"})
	require.NotContains(t, payload, "delta")
	require.NotContains(t, encoded, "secret-audio")
}

func TestLiveTranscriptDeltaKeyIsStable(t *testing.T) {
	sessionID := uuid.New()
	require.Equal(t, transcriptKey(sessionID, "session.output_transcript.delta", map[string]any{"item_id": "item-1"}), transcriptKey(sessionID, "session.output_transcript.done", map[string]any{"item_id": "item-1"}))
}
