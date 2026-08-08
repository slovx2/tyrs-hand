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

func TestRepositoryRejectsWorkspaceExecution(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, db.Close()) })
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	_, inserted, err := NewRepository(db, time.Minute).Enqueue(context.Background(), tx,
		EnqueueRequest{SourceType: "workspace_session"})
	require.ErrorIs(t, err, ErrInvalidSource)
	require.False(t, inserted)
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	_, err = NewRepository(db, time.Minute).ClaimSource(context.Background(), "worker",
		"workspace_session")
	require.ErrorIs(t, err, ErrInvalidSource)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestHeartbeatUpdatesGitHubControlAndRun(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, db.Close()) })
	claimed := &ClaimedControl{Intent: Intent{ID: uuid.New(), ControlID: uuid.New()},
		RunID: uuid.New(), LeaseToken: "lease-token", LeaseEpoch: 3}
	mock.ExpectExec(regexp.QuoteMeta("WITH updated_control AS (")).
		WithArgs(claimed.ControlID, sqlmock.AnyArg(), claimed.LeaseEpoch,
			"90.000000 seconds", claimed.ID, claimed.RunID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, NewRepository(db, 90*time.Second).Heartbeat(context.Background(), claimed))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestHeartbeatRejectsStaleGitHubRun(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, db.Close()) })
	claimed := &ClaimedControl{Intent: Intent{ID: uuid.New(), ControlID: uuid.New()},
		RunID: uuid.New(), LeaseToken: "stale", LeaseEpoch: 4}
	mock.ExpectExec(regexp.QuoteMeta("WITH updated_control AS (")).
		WithArgs(claimed.ControlID, sqlmock.AnyArg(), claimed.LeaseEpoch,
			"30.000000 seconds", claimed.ID, claimed.RunID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	err = NewRepository(db, 30*time.Second).Heartbeat(context.Background(), claimed)
	require.ErrorIs(t, err, ErrLeaseLost)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReconcileExhaustedGitHubJobReleasesLease(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, db.Close()) })
	claimed := &ClaimedControl{Intent: Intent{ID: uuid.New(), ControlID: uuid.New(),
		Attempt: 3, MaxAttempts: 3}, RunID: uuid.New(), LeaseToken: "lease", LeaseEpoch: 2}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT EXISTS").WithArgs(claimed.ControlID, sqlmock.AnyArg(),
		claimed.LeaseEpoch, claimed.ID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("UPDATE codex_turn_intents SET status").
		WithArgs(claimed.ID, IntentFailed, "runtime_error", "failed").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE codex_turn_runs SET status='failed'").
		WithArgs(claimed.RunID, "runtime_error", "failed").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE codex_thread_controls SET status").
		WithArgs(claimed.ControlID, "idle", "runtime_error", "failed").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, NewRepository(db, time.Minute).Reconcile(context.Background(),
		claimed, "runtime_error", errors.New("failed")))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRequeueExpiredOnlyProcessesGitHubJobs(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, db.Close()) })
	controlID, intentID := uuid.New(), uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT control.id,control.active_intent_id").
		WillReturnRows(sqlmock.NewRows([]string{"control_id", "intent_id"}).
			AddRow(controlID, intentID))
	mock.ExpectExec("UPDATE codex_turn_intents SET status='reconciling'").
		WithArgs(intentID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE codex_turn_runs SET status='failed'").
		WithArgs(controlID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE codex_thread_controls SET status='reconciling'").
		WithArgs(controlID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	count, err := NewRepository(db, time.Minute).RequeueExpired(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	require.NoError(t, mock.ExpectationsWereMet())
}
