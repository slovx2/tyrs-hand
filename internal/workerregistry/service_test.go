package workerregistry

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestHasRoleAllRequiresBothRoles(t *testing.T) {
	require.True(t, HasRole(Worker{Roles: []string{"github", "discord"}}, "all"))
	require.False(t, HasRole(Worker{Roles: []string{"github"}}, "all"))
	require.True(t, HasRole(Worker{Roles: []string{"discord"}}, "discord"))
}

func TestListReturnsEmptyArrayWhenNoWorkersExist(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	mock.ExpectQuery("SELECT id, name, roles, enabled, max_concurrent_jobs").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "roles", "enabled",
			"max_concurrent_jobs", "protocol_version", "worker_version", "status",
			"heartbeat_at", "last_error", "metadata"}))

	workers, err := NewService(db).List(context.Background())
	require.NoError(t, err)
	require.NotNil(t, workers)
	require.Empty(t, workers)

	mock.ExpectClose()
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEnrollmentTokenCanOnlyBeConsumedOnce(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	workerID := uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE worker_enrollments SET consumed_at = now()")).
		WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow(workerID))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE workers SET credential_hash = $2")).
		WithArgs(workerID, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id, name, roles, enabled, max_concurrent_jobs").WithArgs(workerID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "roles", "enabled",
			"max_concurrent_jobs", "protocol_version", "worker_version", "status",
			"heartbeat_at", "last_error", "metadata"}).AddRow(workerID, "home", []byte(`["github"]`),
			true, 2, 1, "", "offline", nil, "", []byte(`{}`)))
	mock.ExpectCommit()
	worker, credential, err := NewService(db).Enroll(context.Background(), "one-time-token")
	require.NoError(t, err)
	require.Equal(t, workerID, worker.ID)
	require.NotEmpty(t, credential)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE worker_enrollments SET consumed_at = now()")).
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
	workerID := uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE worker_enrollments").WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow(workerID))
	mock.ExpectExec("UPDATE workers").WithArgs(workerID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	_, _, err = NewService(db).Enroll(context.Background(), "token")
	require.ErrorIs(t, err, ErrDisabled)
	mock.ExpectClose()
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}
