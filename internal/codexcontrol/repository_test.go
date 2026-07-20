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
