package codexcontrol

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestEnqueueRejectsTerminatedControl(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	controlID := uuid.New()
	conversationID := uuid.New()
	profileID := uuid.New()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO codex_thread_controls")).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(controlID))
	mock.ExpectQuery("SELECT control.status,").
		WithArgs(controlID).
		WillReturnRows(sqlmock.NewRows([]string{"status", "lifecycle_state"}).
			AddRow("error", "active"))
	mock.ExpectRollback()
	mock.ExpectClose()

	_, inserted, err := NewRepository(db, time.Minute).Enqueue(context.Background(), tx, EnqueueRequest{
		SourceType: SourceDiscord, DiscordConversationID: conversationID,
		AgentProfileID: profileID, ContextVersion: 1, IdempotencyKey: "discord:message:1",
		Instruction: "retry",
	})
	require.ErrorIs(t, err, ErrControlTerminated)
	require.False(t, inserted)
	require.NoError(t, tx.Rollback())
}

func TestHeartbeatUpdatesControlAndRun(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	claimed := &ClaimedControl{
		Intent: Intent{ID: uuid.New(), ControlID: uuid.New()}, RunID: uuid.New(),
		LeaseToken: "lease-token", LeaseEpoch: 3,
	}
	mock.ExpectExec(regexp.QuoteMeta("WITH updated_control AS (")).
		WithArgs(claimed.ControlID, sqlmock.AnyArg(), claimed.LeaseEpoch,
			"90.000000 seconds", claimed.ID, claimed.RunID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectClose()

	err = NewRepository(db, 90*time.Second).Heartbeat(context.Background(), claimed)
	require.NoError(t, err)
}

func TestHeartbeatRejectsStaleRun(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	claimed := &ClaimedControl{
		Intent: Intent{ID: uuid.New(), ControlID: uuid.New()}, RunID: uuid.New(),
		LeaseToken: "stale-token", LeaseEpoch: 4,
	}
	mock.ExpectExec(regexp.QuoteMeta("WITH updated_control AS (")).
		WithArgs(claimed.ControlID, sqlmock.AnyArg(), claimed.LeaseEpoch,
			"30.000000 seconds", claimed.ID, claimed.RunID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectClose()

	err = NewRepository(db, 30*time.Second).Heartbeat(context.Background(), claimed)
	require.True(t, errors.Is(err, ErrLeaseLost))
}

func TestReconcileExhaustedIntentReturnsControlToIdle(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	claimed := &ClaimedControl{
		Intent: Intent{ID: uuid.New(), ControlID: uuid.New(), Attempt: 3, MaxAttempts: 3},
		RunID:  uuid.New(), LeaseToken: "lease-token", LeaseEpoch: 2,
	}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(SELECT 1 FROM codex_thread_controls")).
		WithArgs(claimed.ControlID, sqlmock.AnyArg(), claimed.LeaseEpoch, claimed.ID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("UPDATE codex_turn_intents SET status").
		WithArgs(claimed.ID, "failed", "desktop_turn_error", "runtime failed").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE codex_turn_runs SET status = 'failed'").
		WithArgs(claimed.RunID, "desktop_turn_error", "runtime failed").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE codex_thread_controls SET status").
		WithArgs(claimed.ControlID, "idle", "desktop_turn_error", "runtime failed").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectClose()

	err = NewRepository(db, time.Minute).Reconcile(context.Background(), claimed,
		"desktop_turn_error", errors.New("runtime failed"))
	require.NoError(t, err)
}

func TestReconcileDesktopIntentReturnsControlToIdleImmediately(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	claimed := &ClaimedControl{
		Intent: Intent{
			ID: uuid.New(), ControlID: uuid.New(), InputSurface: "desktop",
			Attempt: 1, MaxAttempts: 3,
		},
		RunID: uuid.New(), LeaseToken: "lease-token", LeaseEpoch: 2,
	}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(SELECT 1 FROM codex_thread_controls")).
		WithArgs(claimed.ControlID, sqlmock.AnyArg(), claimed.LeaseEpoch, claimed.ID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("UPDATE codex_turn_intents SET status").
		WithArgs(claimed.ID, "failed", "desktop_turn_error", "runtime failed").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE codex_turn_runs SET status = 'failed'").
		WithArgs(claimed.RunID, "desktop_turn_error", "runtime failed").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE codex_thread_controls SET status").
		WithArgs(claimed.ControlID, "idle", "desktop_turn_error", "runtime failed").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectClose()

	err = NewRepository(db, time.Minute).Reconcile(context.Background(), claimed,
		"desktop_turn_error", errors.New("runtime failed"))
	require.NoError(t, err)
}
