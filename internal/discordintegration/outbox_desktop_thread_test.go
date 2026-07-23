package discordintegration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCompleteDesktopThreadPostRejectsMalformedOutboxDataBeforeDatabase(t *testing.T) {
	store := &SQLoutbox{}
	err := store.completeDesktopThreadPost(context.Background(), nil, OutboxItem{
		OperationKey: "desktop-thread-post:not-a-uuid",
	}, json.RawMessage(`{"threadId":"thread","messageId":"message"}`))
	require.ErrorContains(t, err, "operation key 无效")

	err = store.completeDesktopThreadPost(context.Background(), nil, OutboxItem{
		OperationKey: "desktop-thread-post:" + uuid.NewString(),
	}, json.RawMessage(`{"threadId":"","messageId":"message"}`))
	require.ErrorContains(t, err, "Outbox 结果无效")
}

func TestDesktopThreadProjectionReturnsDatabaseFailures(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	store := NewSQLoutbox(db)
	requestID := uuid.New()

	mock.ExpectBegin()
	tx, err := db.Begin()
	require.NoError(t, err)
	queryFailure := errors.New("desktop request query failed")
	mock.ExpectQuery("SELECT r.status").WithArgs(requestID).WillReturnError(queryFailure)
	err = store.completeDesktopThreadPost(context.Background(), tx, OutboxItem{
		OperationKey: "desktop-thread-post:" + requestID.String(),
	}, json.RawMessage(`{"threadId":"thread","messageId":"message"}`))
	require.ErrorIs(t, err, queryFailure)
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())

	mock.ExpectBegin()
	tx, err = db.Begin()
	require.NoError(t, err)
	pendingFailure := errors.New("pending input query failed")
	mock.ExpectQuery("SELECT id, desktop_input_projection_key").
		WithArgs(sqlmock.AnyArg(), "first-key").WillReturnError(pendingFailure)
	err = enqueuePendingDesktopInputs(context.Background(), tx, uuid.New(),
		"thread", uuid.New(), "first-key")
	require.ErrorIs(t, err, pendingFailure)
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())

	replayFailure := errors.New("replay query failed")
	mock.ExpectQuery("SELECT r.control_id").WithArgs(requestID).WillReturnError(replayFailure)
	err = store.replayDesktopProjection(context.Background(), requestID)
	require.ErrorIs(t, err, replayFailure)
	require.NoError(t, mock.ExpectationsWereMet())
}
