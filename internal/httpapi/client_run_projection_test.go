package httpapi

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestConvergeTerminalRunOperations(t *testing.T) {
	for _, status := range []string{"completed", "failed", "canceled"} {
		t.Run(status, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() {
				mock.ExpectClose()
				require.NoError(t, db.Close())
				require.NoError(t, mock.ExpectationsWereMet())
			})
			mock.ExpectBegin()
			tx, err := db.BeginTx(context.Background(), nil)
			require.NoError(t, err)
			runID := uuid.New()
			mock.ExpectExec(regexp.QuoteMeta(`UPDATE run_process_activities
		SET status='failed',updated_at=now()
		WHERE run_id=$1 AND kind='operation' AND status='running'`)).
				WithArgs(runID).WillReturnResult(sqlmock.NewResult(0, 1))
			require.NoError(t, convergeTerminalRunOperationsTx(context.Background(), tx, runID, status))
			mock.ExpectRollback()
			require.NoError(t, tx.Rollback())
		})
	}
}

func TestConvergeRunOperationsLeavesNonTerminalRunUntouched(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	require.NoError(t, convergeTerminalRunOperationsTx(context.Background(), tx, uuid.New(), "running"))
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
}
