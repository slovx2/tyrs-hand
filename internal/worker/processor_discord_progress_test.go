package worker

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestDiscordProgressReporterRebuildsPersistedActions(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	runID := uuid.New()
	payload, err := json.Marshal(map[string]any{"item": map[string]any{
		"id": "tool-1", "type": "mcpToolCall", "status": "completed",
		"server": "github", "tool": "issue_read",
		"arguments": map[string]any{"repo": "tyrs-hand", "number": 12},
		"result":    map[string]any{"content": "不能进入卡片"},
	}})
	require.NoError(t, err)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT event_type, payload, occurred_at")).
		WithArgs(runID).
		WillReturnRows(sqlmock.NewRows([]string{"event_type", "payload", "occurred_at"}).
			AddRow("item/completed", payload, time.Now().Add(-time.Minute)))
	processor := &Processor{db: db, logger: zap.NewNop()}
	reporter := processor.newDiscordProgressReporter(context.Background(), &codexcontrol.ClaimedControl{
		RunID: runID,
	}, discordJobContext{})

	detail := reporter.detail("本轮处理完成。", 60_000)
	require.Contains(t, detail, "已调用 `github.issue_read`")
	require.Contains(t, detail, "`number=12`")
	require.NotContains(t, detail, "不能进入卡片")
	mock.ExpectClose()
}
