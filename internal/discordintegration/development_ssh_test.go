package discordintegration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql/driver"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestSaveDevelopmentEnvironmentSSHQueuesReconfigure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	public, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key, err := ssh.NewPublicKey(public)
	require.NoError(t, err)
	publicKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " admin"
	environmentID, nodeID := uuid.New(), uuid.New()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE discord_development_environments SET")).
		WithArgs(environmentID, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))),
			ssh.FingerprintSHA256(key), 2222).
		WillReturnRows(sqlmock.NewRows([]string{"execution_node_id", "ssh_config_revision"}).AddRow(nodeID, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO discord_development_operations")).
		WithArgs(environmentID, nodeID).
		WillReturnResult(driver.RowsAffected(1))
	mock.ExpectCommit()

	fingerprint, err := NewManager(db, nil).SaveDevelopmentEnvironmentSSH(context.Background(),
		environmentID, DevelopmentEnvironmentSSHInput{PublicKey: publicKey, Port: 2222})
	require.NoError(t, err)
	require.Equal(t, ssh.FingerprintSHA256(key), fingerprint)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSaveDevelopmentEnvironmentSSHValidatesPortBeforeDatabase(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = NewManager(db, nil).SaveDevelopmentEnvironmentSSH(context.Background(), uuid.New(),
		DevelopmentEnvironmentSSHInput{PublicKey: "invalid", Port: 70000})
	require.ErrorContains(t, err, "端口")
	require.NoError(t, mock.ExpectationsWereMet())
}
