package discordintegration

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestParseProgressButtonActions(t *testing.T) {
	runID := uuid.New()
	for _, action := range []string{"older", "newer", "latest"} {
		parsedAction, parsedRun, page, err := parseProgressButton(
			progressButtonID(action, runID.String(), 2))
		require.NoError(t, err)
		require.Equal(t, action, parsedAction)
		require.Equal(t, runID, parsedRun)
		require.Equal(t, 2, page)
	}
	for _, value := range []string{
		"older:" + runID.String() + ":1",
		"codex-progress-page:" + runID.String() + ":1",
		"codex-progress-older:bad:1",
		"codex-progress-older:" + runID.String() + ":-1",
	} {
		_, _, _, err := parseProgressButton(value)
		require.Error(t, err)
	}
}

func TestConversationProgressPageValidatesMessageAndRun(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	runID := uuid.New()
	desired, err := json.Marshal(map[string]any{"progress": conversationProgressPayload{
		RunID: runID.String(), State: ConversationCompleted, Summary: "本轮完成", Page: 1,
	}})
	require.NoError(t, err)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT desired_payload")).
		WithArgs("guild", "channel", "message").
		WillReturnRows(sqlmock.NewRows([]string{"desired_payload"}).AddRow(desired))
	started := time.Now().Add(-time.Minute)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT started_at, finished_at FROM codex_turn_runs WHERE id = $1")).
		WithArgs(runID).WillReturnRows(sqlmock.NewRows([]string{"started_at", "finished_at"}).
		AddRow(started, time.Now()))
	commentary, err := json.Marshal(map[string]any{"item": map[string]any{
		"id": "commentary", "type": "agentMessage", "phase": "commentary",
		"text": strings.Repeat("需要分页的过程说明。", 600),
	}})
	require.NoError(t, err)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT event_type, payload FROM agent_events")).
		WithArgs(runID).WillReturnRows(sqlmock.NewRows([]string{"event_type", "payload"}).
		AddRow("item/completed", commentary))
	connector := &DisgoConnector{manager: &Manager{db: db}}
	card, err := connector.conversationProgressPage(context.Background(), "guild", "channel",
		"message", runID, 0)
	require.NoError(t, err)
	require.Contains(t, card.Header, "已完成")
	require.NotEmpty(t, card.Timeline)
	require.Contains(t, card.Footer, "第 1 /")
	require.Len(t, card.Buttons, 4)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConversationProgressPageRejectsStaleOrForgedTargets(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	runID := uuid.New()
	desired, err := json.Marshal(map[string]any{"progress": conversationProgressPayload{
		RunID: uuid.NewString(), State: ConversationRunning, Summary: "running",
	}})
	require.NoError(t, err)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT desired_payload")).
		WithArgs("guild", "channel", "message").
		WillReturnRows(sqlmock.NewRows([]string{"desired_payload"}).AddRow(desired))
	connector := &DisgoConnector{manager: &Manager{db: db}}
	_, err = connector.conversationProgressPage(context.Background(), "guild", "channel",
		"message", runID, 0)
	require.ErrorContains(t, err, "不匹配")
	require.NoError(t, mock.ExpectationsWereMet())
}
