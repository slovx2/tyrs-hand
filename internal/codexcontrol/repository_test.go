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
	sessionID := uuid.New()
	workspaceID := uuid.New()
	projectID := uuid.New()
	profileID := uuid.New()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM workspace_sessions")).
		WithArgs(sessionID).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(sessionID))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT environment.worker_id::text")).
		WithArgs(sessionID).WillReturnRows(sqlmock.NewRows([]string{
		"worker_id", "workspace_id", "workspace_project_id", "agent_profile_id",
	}).AddRow(nil, workspaceID, projectID, profileID))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO codex_thread_controls")).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(controlID))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE codex_thread_controls control SET")).
		WithArgs(sessionID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE discord_conversations conversation SET")).
		WithArgs(sessionID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE codex_thread_controls control SET")).
		WithArgs(controlID, conversationID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT control.status,").
		WithArgs(controlID).
		WillReturnRows(sqlmock.NewRows([]string{"status", "lifecycle_state"}).
			AddRow("error", "active"))
	mock.ExpectRollback()
	mock.ExpectClose()

	_, inserted, err := NewRepository(db, time.Minute).Enqueue(context.Background(), tx, EnqueueRequest{
		SourceType: SourceWorkspace, SessionID: sessionID, DiscordConversationID: conversationID,
		AgentProfileID: profileID, IdempotencyKey: "discord:message:1",
		Instruction: "retry",
	})
	require.ErrorIs(t, err, ErrControlTerminated)
	require.False(t, inserted)
	require.NoError(t, tx.Rollback())
}

func TestClaimEntryPointsAndOptionalEncoding(t *testing.T) {
	require.Nil(t, encodeOptional(nil))
	require.JSONEq(t, `{"value":"ok"}`, string(encodeOptional(map[string]string{"value": "ok"})))

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	beginError := errors.New("begin failed")
	mock.ExpectBegin().WillReturnError(beginError)
	_, err = NewRepository(db, time.Minute).Claim(context.Background(), "worker-1")
	require.ErrorIs(t, err, beginError)
	mock.ExpectBegin().WillReturnError(beginError)
	_, err = NewRepository(db, time.Minute).ClaimSource(context.Background(), "worker-1",
		SourceWorkspace)
	require.ErrorIs(t, err, beginError)
	mock.ExpectClose()
}

func TestEnqueueWorkspaceUsesSessionUniqueControl(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	controlID := uuid.New()
	conversationID := uuid.New()
	sessionID := uuid.New()
	workspaceID := uuid.New()
	projectID := uuid.New()
	profileID := uuid.New()
	intentID := uuid.New()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM workspace_sessions")).
		WithArgs(sessionID).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(sessionID))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT environment.worker_id::text")).
		WithArgs(sessionID).WillReturnRows(sqlmock.NewRows([]string{
		"worker_id", "workspace_id", "workspace_project_id", "agent_profile_id",
	}).AddRow(nil, workspaceID, projectID, profileID))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO codex_thread_controls")).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(controlID))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE codex_thread_controls control SET")).
		WithArgs(sessionID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE discord_conversations conversation SET")).
		WithArgs(sessionID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE codex_thread_controls control SET")).
		WithArgs(controlID, conversationID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT control.status,").WithArgs(controlID).
		WillReturnRows(sqlmock.NewRows([]string{"status", "lifecycle_state"}).
			AddRow("idle", "active"))
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE codex_thread_controls")).
		WithArgs(controlID).WillReturnRows(sqlmock.NewRows([]string{"sequence_no"}).AddRow(7))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO codex_turn_intents(")).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(intentID))
	mock.ExpectRollback()

	actual, inserted, err := NewRepository(db, time.Minute).Enqueue(context.Background(), tx,
		EnqueueRequest{
			SourceType: SourceWorkspace, SessionID: sessionID, DiscordConversationID: conversationID,
			AgentProfileID: profileID,
			IdempotencyKey: "discord:message:continue", Instruction: "continue",
		})
	require.NoError(t, err)
	require.True(t, inserted)
	require.Equal(t, intentID, actual)
	require.NoError(t, tx.Rollback())
}

func TestHeartbeatOnlyRecordsWorkerObservation(t *testing.T) {
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
	mock.ExpectExec(regexp.QuoteMeta("UPDATE codex_turn_runs SET heartbeat_at=now()")).
		WithArgs(claimed.RunID, claimed.ControlID, claimed.ID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectClose()

	err = NewRepository(db, 90*time.Second).Heartbeat(context.Background(), claimed)
	require.NoError(t, err)
}

func TestHeartbeatRejectsUnknownWorkerRun(t *testing.T) {
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
	mock.ExpectExec(regexp.QuoteMeta("UPDATE codex_turn_runs SET heartbeat_at=now()")).
		WithArgs(claimed.RunID, claimed.ControlID, claimed.ID).
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
		Intent: Intent{
			ID: uuid.New(), ControlID: uuid.New(), Attempt: 3, MaxAttempts: 3,
			ConfirmedTurnID: "turn-1",
		},
		RunID: uuid.New(), LeaseToken: "lease-token", LeaseEpoch: 2,
	}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(SELECT 1 FROM codex_turn_runs")).
		WithArgs(claimed.RunID, claimed.ControlID, claimed.ID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("UPDATE codex_turn_intents SET status").
		WithArgs(claimed.ID, "failed", "desktop_turn_error", "runtime failed").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE codex_turn_intents SET status = 'failed'").
		WithArgs(claimed.ControlID, claimed.ID, "desktop_turn_error", "runtime failed",
			claimed.ConfirmedTurnID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE codex_turn_runs SET status = 'failed'").
		WithArgs(claimed.RunID, "desktop_turn_error", "runtime failed").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE codex_interactive_requests SET status='interrupted'").
		WithArgs(claimed.RunID).WillReturnResult(sqlmock.NewResult(0, 1))
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
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(SELECT 1 FROM codex_turn_runs")).
		WithArgs(claimed.RunID, claimed.ControlID, claimed.ID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("UPDATE codex_turn_intents SET status").
		WithArgs(claimed.ID, "failed", "desktop_turn_error", "runtime failed").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE codex_turn_intents SET status = 'failed'").
		WithArgs(claimed.ControlID, claimed.ID, "desktop_turn_error", "runtime failed", "").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("UPDATE codex_turn_runs SET status = 'failed'").
		WithArgs(claimed.RunID, "desktop_turn_error", "runtime failed").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE codex_interactive_requests SET status='interrupted'").
		WithArgs(claimed.RunID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE codex_thread_controls SET status").
		WithArgs(claimed.ControlID, "idle", "desktop_turn_error", "runtime failed").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectClose()

	err = NewRepository(db, time.Minute).Reconcile(context.Background(), claimed,
		"desktop_turn_error", errors.New("runtime failed"))
	require.NoError(t, err)
}

func TestCancelFinishesSteerIntents(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	claimed := &ClaimedControl{
		Intent: Intent{
			ID: uuid.New(), ControlID: uuid.New(), ConfirmedTurnID: "turn-1",
		},
		RunID: uuid.New(), LeaseToken: "lease-token", LeaseEpoch: 2,
	}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(SELECT 1 FROM codex_turn_runs")).
		WithArgs(claimed.RunID, claimed.ControlID, claimed.ID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("UPDATE codex_turn_intents SET status = \\$2").
		WithArgs(claimed.ID, IntentCanceled, nil, "user_interrupt", "stopped").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE codex_turn_intents SET status = \\$4").
		WithArgs(claimed.ControlID, claimed.ID, claimed.ConfirmedTurnID,
			IntentCanceled, "user_interrupt", "stopped").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE codex_turn_runs SET status = \\$2").
		WithArgs(claimed.RunID, IntentCanceled, "user_interrupt", "stopped").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE codex_interactive_requests SET status='interrupted'").
		WithArgs(claimed.RunID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE codex_thread_controls SET status = \\$2").
		WithArgs(claimed.ControlID, "idle", "user_interrupt", "stopped").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectClose()

	err = NewRepository(db, time.Minute).Cancel(context.Background(), claimed,
		"user_interrupt", "stopped")
	require.NoError(t, err)
}

func TestNonRetryableCodexErrorFinishesImmediatelyAndPersistsDetails(t *testing.T) {
	for _, test := range []struct {
		name          string
		sourceType    string
		controlStatus string
	}{
		{name: "workspace remains available", sourceType: SourceWorkspace, controlStatus: "idle"},
		{name: "github work item terminates", sourceType: SourceGitHub, controlStatus: "error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, db.Close())
				require.NoError(t, mock.ExpectationsWereMet())
			})
			claimed := &ClaimedControl{Intent: Intent{
				ID: uuid.New(), ControlID: uuid.New(), ConfirmedTurnID: "turn-1",
				SourceType: test.sourceType,
			}, RunID: uuid.New(), LeaseToken: "lease-token", LeaseEpoch: 2}
			codexError := map[string]any{"message": "at capacity", "willRetry": false,
				"threadId": "thread-1", "turnId": "turn-1"}
			encoded := encode(codexError)

			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(SELECT 1 FROM codex_turn_runs")).
				WithArgs(claimed.RunID, claimed.ControlID, claimed.ID).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
			mock.ExpectExec("UPDATE codex_turn_intents SET status = \\$2").
				WithArgs(claimed.ID, IntentFailed, nil, "codex_non_retryable_error", "at capacity").
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("UPDATE codex_turn_intents SET status = \\$4").
				WithArgs(claimed.ControlID, claimed.ID, claimed.ConfirmedTurnID, IntentFailed,
					"codex_non_retryable_error", "at capacity").
				WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectExec("UPDATE codex_turn_runs SET status = \\$2").
				WithArgs(claimed.RunID, IntentFailed, "codex_non_retryable_error", "at capacity", encoded).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("UPDATE codex_interactive_requests SET status='interrupted'").
				WithArgs(claimed.RunID).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("UPDATE codex_thread_controls SET status = \\$2").
				WithArgs(claimed.ControlID, test.controlStatus,
					"codex_non_retryable_error", "at capacity").
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			mock.ExpectClose()

			err = NewRepository(db, time.Minute).FailWithCodexError(context.Background(), claimed,
				"codex_non_retryable_error", errors.New("at capacity"), codexError)
			require.NoError(t, err)
		})
	}
}

func TestRequeueExpiredDoesNotChangeWorkerRuns(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	mock.ExpectClose()

	requeued, err := NewRepository(db, time.Minute).RequeueExpired(context.Background())
	require.NoError(t, err)
	require.Zero(t, requeued)
}
