package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/stretchr/testify/require"
)

func TestValidateDiscordPostFingerprintsRejectsSelectedCollision(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	payload := reconciliationPayload("same")
	fingerprint, err := discordintegration.ForumPostRequestFingerprint(payload, "900")
	require.NoError(t, err)
	err = validateDiscordPostFingerprints(context.Background(), db, "900", []discordPostRepair{
		{requestID: "request-1", operationKey: "operation-1", routeKey: "forum-1", fingerprint: fingerprint},
		{requestID: "request-2", operationKey: "operation-2", routeKey: "forum-1", fingerprint: fingerprint},
	})
	require.ErrorContains(t, err, "语义指纹相同")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestValidateDiscordPostFingerprintsRejectsUnselectedCollision(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	payload := reconciliationPayload("same")
	fingerprint, err := discordintegration.ForumPostRequestFingerprint(payload, "900")
	require.NoError(t, err)
	mock.ExpectQuery("SELECT operation_key").WillReturnRows(sqlmock.NewRows(
		[]string{"operation_key", "route_key", "payload"}).
		AddRow("other-operation", "forum-1", payload))
	err = validateDiscordPostFingerprints(context.Background(), db, "900", []discordPostRepair{{
		requestID: "request-1", operationKey: "operation-1", routeKey: "forum-1",
		fingerprint: fingerprint,
	}})
	require.ErrorContains(t, err, "另一条 Outbox")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestValidateDiscordPostFingerprintsAllowsDistinctOrOtherForum(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	payload := reconciliationPayload("target")
	fingerprint, err := discordintegration.ForumPostRequestFingerprint(payload, "900")
	require.NoError(t, err)
	mock.ExpectQuery("SELECT operation_key").WillReturnRows(sqlmock.NewRows(
		[]string{"operation_key", "route_key", "payload"}).
		AddRow("same-payload-other-forum", "forum-2", payload).
		AddRow("different-payload", "forum-1", reconciliationPayload("different")))
	err = validateDiscordPostFingerprints(context.Background(), db, "900", []discordPostRepair{{
		requestID: "request-1", operationKey: "operation-1", routeKey: "forum-1",
		fingerprint: fingerprint,
	}})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func reconciliationPayload(body string) json.RawMessage {
	encoded, _ := json.Marshal(map[string]any{
		"threadName": "Desktop", "card": discordintegration.ComponentCardPayload{
			AccentColor: 0x5865F2, Header: "Input", Body: body,
		},
	})
	return encoded
}
