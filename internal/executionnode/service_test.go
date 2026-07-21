package executionnode

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestEnrollmentTokenCanOnlyBeConsumedOnce(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	nodeID := uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE execution_node_enrollments SET consumed_at = now()")).
		WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow(nodeID))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE execution_nodes SET credential_hash = $2")).
		WithArgs(nodeID, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id, name, roles, enabled, max_concurrent_jobs").WithArgs(nodeID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "roles", "enabled",
			"max_concurrent_jobs", "protocol_version", "worker_version", "status",
			"heartbeat_at", "last_error", "metadata"}).AddRow(nodeID, "home", []byte(`["github"]`),
			true, 2, 1, "", "offline", nil, "", []byte(`{}`)))
	mock.ExpectCommit()
	node, credential, err := NewService(db).Enroll(context.Background(), "one-time-token")
	require.NoError(t, err)
	require.Equal(t, nodeID, node.ID)
	require.NotEmpty(t, credential)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE execution_node_enrollments SET consumed_at = now()")).
		WithArgs(sqlmock.AnyArg()).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	_, _, err = NewService(db).Enroll(context.Background(), "one-time-token")
	require.ErrorIs(t, err, ErrUnauthorized)
	mock.ExpectClose()
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDisabledNodeCannotRotateCredential(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	nodeID := uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE execution_node_enrollments").WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow(nodeID))
	mock.ExpectExec("UPDATE execution_nodes").WithArgs(nodeID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	_, _, err = NewService(db).Enroll(context.Background(), "token")
	require.ErrorIs(t, err, ErrDisabled)
	mock.ExpectClose()
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}
