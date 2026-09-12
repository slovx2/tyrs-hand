package httpapi

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
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

func TestLivePayloadSanitizesNestedProviderSecrets(t *testing.T) {
	payload := sanitizeLivePayload(map[string]any{
		"type":    "session.updated",
		"session": map[string]any{"id": "remote-session", "model": "gpt-live"},
		"item":    map[string]any{"id": "item-1", "audioBase64": "secret-audio", "text": "hello"},
	}, false).(map[string]any)
	session := payload["session"].(map[string]any)
	item := payload["item"].(map[string]any)
	require.NotContains(t, session, "id")
	require.Equal(t, "item-1", item["id"])
	require.NotContains(t, item, "audioBase64")
}

func TestLiveTranscriptReadsNestedProviderFields(t *testing.T) {
	event := map[string]any{"type": "output_transcript.added", "item": map[string]any{"id": "item-2", "transcript": "hello"}}
	require.Equal(t, "hello", eventText(event))
}

func TestTranscriptCompletionPrefersAccumulatedDoneText(t *testing.T) {
	event := map[string]any{"delta": "last fragment"}
	require.Equal(t, "all fragments", transcriptCompletionText("done", "all fragments", event))
	require.Equal(t, "last fragment", transcriptCompletionText("done", "", event))
}

func TestLivePayloadSanitizesTopLevelProviderID(t *testing.T) {
	payload := sanitizeLivePayload(map[string]any{
		"type":    "session.started",
		"id":      "remote-session-id",
		"session": map[string]any{"id": "remote-session-id", "model": "gpt-live-1"},
	}, false).(map[string]any)
	require.NotContains(t, payload, "id")
	require.NotContains(t, payload["session"], "id")
}

func TestTerminalLiveSessionStatus(t *testing.T) {
	require.True(t, isTerminalLiveSessionStatus("replaced"))
	require.True(t, isTerminalLiveSessionStatus("closed"))
	require.False(t, isTerminalLiveSessionStatus("closing"))
}
func TestLiveManagerDoesNotProjectLateEventFromReplacedSession(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	sessionID := uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT status FROM live_sessions WHERE id=$1 FOR UPDATE`)).
		WithArgs(sessionID).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("replaced"))
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO live_events(live_session_id,direction,event_type,event_id,dedupe_key,payload)`)).
		WithArgs(sessionID, "server", "output_transcript.added", "event-1", "event-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"inserted"}).AddRow(true))
	mock.ExpectCommit()

	manager := newLiveManager(db, nil, nil)
	err = manager.persistEvent(context.Background(), sessionID, "server", map[string]any{
		"type": "output_transcript.added", "id": "event-1", "text": "stale",
	})
	require.NoError(t, err)
	manager.mu.Lock()
	require.Empty(t, manager.transcripts)
	manager.mu.Unlock()
}
