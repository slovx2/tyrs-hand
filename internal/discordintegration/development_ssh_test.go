package discordintegration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"errors"
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
	discordUserID := "100000000000000001"

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(")).
		WithArgs(environmentID, discordUserID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE discord_development_environments SET")).
		WithArgs(environmentID, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))),
			ssh.FingerprintSHA256(key), 2222, discordUserID).
		WillReturnRows(sqlmock.NewRows([]string{"execution_node_id", "ssh_config_revision"}).AddRow(nodeID, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO discord_development_operations")).
		WithArgs(environmentID, nodeID).
		WillReturnResult(driver.RowsAffected(1))
	mock.ExpectCommit()

	fingerprint, err := NewManager(db, nil, "").SaveDevelopmentEnvironmentSSH(context.Background(),
		environmentID, DevelopmentEnvironmentSSHInput{
			PublicKey: publicKey, Port: 2222, DiscordUserID: discordUserID,
		})
	require.NoError(t, err)
	require.Equal(t, ssh.FingerprintSHA256(key), fingerprint)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSaveDevelopmentEnvironmentSSHValidatesPortBeforeDatabase(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = NewManager(db, nil, "").SaveDevelopmentEnvironmentSSH(context.Background(), uuid.New(),
		DevelopmentEnvironmentSSHInput{
			PublicKey: "invalid", Port: 70000, DiscordUserID: "100000000000000001",
		})
	require.ErrorContains(t, err, "端口")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSaveDevelopmentEnvironmentSSHRejectsInactiveOrForeignMember(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	public, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key, err := ssh.NewPublicKey(public)
	require.NoError(t, err)
	environmentID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(")).
		WithArgs(environmentID, "100000000000000099").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()

	_, err = NewManager(db, nil, "").SaveDevelopmentEnvironmentSSH(context.Background(),
		environmentID, DevelopmentEnvironmentSSHInput{
			PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))),
			Port:      2222, DiscordUserID: "100000000000000099",
		})
	require.ErrorContains(t, err, "活跃 Discord 成员")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSaveDevelopmentEnvironmentSSHRejectsInvalidIdentityAndKeyBeforeDatabase(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	manager := NewManager(db, nil, "")

	_, err = manager.SaveDevelopmentEnvironmentSSH(context.Background(), uuid.New(),
		DevelopmentEnvironmentSSHInput{
			PublicKey: "invalid", Port: 2222, DiscordUserID: "not-a-snowflake",
		})
	require.ErrorContains(t, err, "有效的 Discord 成员")

	_, err = manager.SaveDevelopmentEnvironmentSSH(context.Background(), uuid.New(),
		DevelopmentEnvironmentSSHInput{
			PublicKey: "invalid", Port: 2222, DiscordUserID: "100000000000000001",
		})
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSaveDevelopmentEnvironmentSSHRejectsUnavailableEnvironment(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	public, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key, err := ssh.NewPublicKey(public)
	require.NoError(t, err)
	environmentID := uuid.New()
	discordUserID := "100000000000000001"

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(")).
		WithArgs(environmentID, discordUserID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE discord_development_environments SET")).
		WithArgs(environmentID, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))),
			ssh.FingerprintSHA256(key), 2222, discordUserID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err = NewManager(db, nil, "").SaveDevelopmentEnvironmentSSH(context.Background(),
		environmentID, DevelopmentEnvironmentSSHInput{
			PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))),
			Port:      2222, DiscordUserID: discordUserID,
		})
	require.ErrorContains(t, err, "开发环境不存在")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSaveDevelopmentEnvironmentSSHRollsBackWhenReconfigureQueueFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	public, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key, err := ssh.NewPublicKey(public)
	require.NoError(t, err)
	environmentID, nodeID := uuid.New(), uuid.New()
	discordUserID := "100000000000000001"
	queueErr := errors.New("queue unavailable")

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(")).
		WithArgs(environmentID, discordUserID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE discord_development_environments SET")).
		WithArgs(environmentID, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))),
			ssh.FingerprintSHA256(key), 2222, discordUserID).
		WillReturnRows(sqlmock.NewRows([]string{
			"execution_node_id", "ssh_config_revision",
		}).AddRow(nodeID, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO discord_development_operations")).
		WithArgs(environmentID, nodeID).
		WillReturnError(queueErr)
	mock.ExpectRollback()

	_, err = NewManager(db, nil, "").SaveDevelopmentEnvironmentSSH(context.Background(),
		environmentID, DevelopmentEnvironmentSSHInput{
			PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))),
			Port:      2222, DiscordUserID: discordUserID,
		})
	require.ErrorIs(t, err, queueErr)
	require.NoError(t, mock.ExpectationsWereMet())
}
