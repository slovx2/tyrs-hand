package auth

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/security"
	"github.com/stretchr/testify/require"
)

func TestAuthenticateRestoresStoredCSRF(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	service := NewService(db, box, "", "")
	encrypted, err := service.encryptCSRF("csrf-token")
	require.NoError(t, err)
	token := "session-token"
	expiresAt := time.Now().Add(time.Hour)
	administratorID := uuid.New()
	mock.ExpectQuery("UPDATE admin_sessions").
		WithArgs(security.Digest(token)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "username", "expires_at", "csrf_token_ciphertext",
		}).AddRow(administratorID, "admin", expiresAt, encrypted))

	session, err := service.Authenticate(t.Context(), token)
	require.NoError(t, err)
	require.Equal(t, administratorID, session.AdministratorID)
	require.Equal(t, "csrf-token", session.CSRFToken)
}

func TestAuthenticateIssuesCSRFForExistingSession(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	box, err := security.NewSecretBox(make([]byte, 32))
	require.NoError(t, err)
	service := NewService(db, box, "", "")
	token := "existing-session"
	tokenHash := security.Digest(token)
	mock.ExpectQuery("UPDATE admin_sessions").
		WithArgs(tokenHash).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "username", "expires_at", "csrf_token_ciphertext",
		}).AddRow(uuid.New(), "admin", time.Now().Add(time.Hour), nil))
	mock.ExpectExec("UPDATE admin_sessions").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), tokenHash).
		WillReturnResult(sqlmock.NewResult(0, 1))

	session, err := service.Authenticate(t.Context(), token)
	require.NoError(t, err)
	require.NotEmpty(t, session.CSRFToken)
}
