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

func expectPersistActiveEvent(mock sqlmock.Sqlmock, sessionID uuid.UUID, typ, eventID string) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT status FROM live_sessions WHERE id=$1 FOR UPDATE`)).
		WithArgs(sessionID).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("active"))
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO live_events(live_session_id,direction,event_type,event_id,dedupe_key,payload)`)).
		WithArgs(sessionID, "server", typ, eventID, eventID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"inserted"}).AddRow(true))
}

func expectAppendLiveMessage(mock sqlmock.Sqlmock, sessionID, conversationID uuid.UUID, role, text, sourceEventID string) {
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT conversation_id FROM live_sessions WHERE id=$1 FOR SHARE`)).
		WithArgs(sessionID).
		WillReturnRows(sqlmock.NewRows([]string{"conversation_id"}).AddRow(conversationID))
	mock.ExpectExec(regexp.QuoteMeta(`SELECT pg_advisory_xact_lock(hashtext($1))`)).
		WithArgs(conversationID.String()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS(SELECT 1 FROM live_messages WHERE source_session_id=$1 AND source_event_id=$2)`)).
		WithArgs(sessionID, sourceEventID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO live_messages(conversation_id,sequence,role,text,source_session_id,source_event_id)`)).
		WithArgs(conversationID, role, text, sessionID, sourceEventID).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE live_conversations SET context_revision=context_revision+1,updated_at=now() WHERE id=$1`)).
		WithArgs(conversationID).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestLiveManagerDoesNotInsertAddedTranscriptFragments(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	sessionID := uuid.New()
	expectPersistActiveEvent(mock, sessionID, "input_transcript.added", "added-1")
	mock.ExpectCommit()
	expectPersistActiveEvent(mock, sessionID, "input_transcript.added", "added-2")
	mock.ExpectCommit()

	manager := newLiveManager(db, nil, nil)
	require.NoError(t, manager.persistEvent(context.Background(), sessionID, "server", map[string]any{
		"type": "input_transcript.added", "id": "added-1",
		"item": map[string]any{"id": "item-1", "text": "这是"},
	}))
	require.NoError(t, manager.persistEvent(context.Background(), sessionID, "server", map[string]any{
		"type": "input_transcript.added", "id": "added-2",
		"item": map[string]any{"id": "item-2", "text": "泰"},
	}))
	manager.mu.Lock()
	require.Equal(t, "这是泰", manager.transcripts[liveTranscriptKey(sessionID, "user", "open")])
	manager.mu.Unlock()
}

func TestLiveManagerInsertsOneMessageOnTurnDone(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	sessionID := uuid.New()
	conversationID := uuid.New()
	expectPersistActiveEvent(mock, sessionID, "input_transcript.added", "added-1")
	mock.ExpectCommit()
	expectPersistActiveEvent(mock, sessionID, "turn.done", "done-1")
	expectAppendLiveMessage(mock, sessionID, conversationID, "user", "这是泰尔斯·汉德实时语音验收,请重复这句话", "transcript:turn:turn-1")
	mock.ExpectCommit()

	manager := newLiveManager(db, nil, nil)
	require.NoError(t, manager.persistEvent(context.Background(), sessionID, "server", map[string]any{
		"type": "input_transcript.added", "id": "added-1",
		"item": map[string]any{"id": "item-1", "text": "这是"},
	}))
	require.NoError(t, manager.persistEvent(context.Background(), sessionID, "server", map[string]any{
		"type": "turn.done", "id": "done-1",
		"turn": map[string]any{"id": "turn-1", "role": "user", "transcript": "这是泰尔斯·汉德实时语音验收,请重复这句话"},
	}))
	manager.mu.Lock()
	require.NotContains(t, manager.transcripts, liveTranscriptKey(sessionID, "user", "open"))
	require.NotContains(t, manager.transcripts, liveTranscriptKey(sessionID, "user", "turn-1"))
	manager.mu.Unlock()
}

func TestLiveManagerInsertsOneMessageOnPublicTranscriptDone(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	sessionID := uuid.New()
	conversationID := uuid.New()
	expectPersistActiveEvent(mock, sessionID, "session.output_transcript.delta", "delta-1")
	mock.ExpectCommit()
	expectPersistActiveEvent(mock, sessionID, "session.output_transcript.done", "done-1")
	expectAppendLiveMessage(mock, sessionID, conversationID, "assistant", "hello ", "transcript:"+liveTranscriptKey(sessionID, "assistant", "item-1"))
	mock.ExpectCommit()

	manager := newLiveManager(db, nil, nil)
	require.NoError(t, manager.persistEvent(context.Background(), sessionID, "server", map[string]any{
		"type": "session.output_transcript.delta", "id": "delta-1", "item_id": "item-1", "delta": "hello ",
	}))
	require.NoError(t, manager.persistEvent(context.Background(), sessionID, "server", map[string]any{
		"type": "session.output_transcript.done", "id": "done-1", "item_id": "item-1", "text": "ignored",
	}))
}

func TestLiveManagerInsertsAssistantTurnDone(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	sessionID := uuid.New()
	conversationID := uuid.New()
	expectPersistActiveEvent(mock, sessionID, "output_transcript.added", "added-1")
	mock.ExpectCommit()
	expectPersistActiveEvent(mock, sessionID, "turn.done", "done-1")
	expectAppendLiveMessage(mock, sessionID, conversationID, "assistant", "这是泰尔斯·汉德实时语音验收,请重复这句话", "transcript:turn:turn-2")
	mock.ExpectCommit()

	manager := newLiveManager(db, nil, nil)
	require.NoError(t, manager.persistEvent(context.Background(), sessionID, "server", map[string]any{
		"type": "output_transcript.added", "id": "added-1",
		"item": map[string]any{"id": "item-9", "text": "这是"},
	}))
	require.NoError(t, manager.persistEvent(context.Background(), sessionID, "server", map[string]any{
		"type": "turn.done", "id": "done-1",
		"turn": map[string]any{"id": "turn-2", "role": "assistant", "transcript": "这是泰尔斯·汉德实时语音验收,请重复这句话"},
	}))
}
