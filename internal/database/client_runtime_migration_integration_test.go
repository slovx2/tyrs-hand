//go:build integration

package database

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClientRuntimeMigrationPreservesCodexAuthorization(t *testing.T) {
	db := migrationTestDatabase(t)
	ctx := t.Context()
	_, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (
 version text PRIMARY KEY,checksum char(64) NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())`)
	require.NoError(t, err)
	migrations, err := loadMigrations()
	require.NoError(t, err)
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	for _, item := range migrations {
		if strings.HasPrefix(item.version, "033_") {
			break
		}
		require.NoError(t, applyTransactional(ctx, conn, item))
	}
	require.NoError(t, conn.Close())
	_, err = db.ExecContext(ctx, `
 INSERT INTO administrators(username,password_hash,totp_secret_ciphertext) VALUES ('legacy','hash','');
 INSERT INTO workers(name) VALUES ('legacy');
 INSERT INTO client_devices(id,administrator_id,name,platform,credential_hash)
 SELECT gen_random_uuid(),id,'旧手机','ios','retained-credential' FROM administrators;
 INSERT INTO client_device_workers(device_id,worker_id,ssh_host_key_fingerprint)
 SELECT device.id,worker.id,'SHA256:'||repeat('a',43) FROM client_devices device,workers worker;
 INSERT INTO client_device_pairings(administrator_id,pairing_secret_hash,worker_id,ssh_host_key_fingerprint,expires_at)
 SELECT admin.id,'retained-pairing',worker.id,'SHA256:'||repeat('a',43),now()+interval '1 hour'
 FROM administrators admin,workers worker;`)
	require.NoError(t, err)
	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, Migrate(ctx, db))
	for _, table := range []string{"client_device_workers", "client_device_pairings"} {
		var total, codex int
		require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*),count(*) FILTER (WHERE engine='codex') FROM "+table).Scan(&total, &codex))
		require.Equal(t, 1, total)
		require.Equal(t, total, codex)
	}
	var credential, fingerprint, pairing string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT device.credential_hash,binding.ssh_host_key_fingerprint,
 pairing.pairing_secret_hash FROM client_devices device JOIN client_device_workers binding ON binding.device_id=device.id
 JOIN client_device_pairings pairing ON pairing.worker_id=binding.worker_id`).Scan(&credential, &fingerprint, &pairing))
	require.Equal(t, "retained-credential", credential)
	require.Equal(t, "SHA256:"+strings.Repeat("a", 43), fingerprint)
	require.Equal(t, "retained-pairing", pairing)
	_, err = db.ExecContext(ctx, `INSERT INTO client_device_workers(device_id,worker_id,engine,ssh_host_key_fingerprint)
 SELECT device_id,worker_id,'claude-code','SHA256:'||repeat('b',43) FROM client_device_workers`)
	require.NoError(t, err, "同一设备可以增加另一个引擎授权")
	_, err = db.ExecContext(ctx, `UPDATE client_device_workers SET engine='claude-code' WHERE engine='codex'`)
	require.Error(t, err, "原授权不能迁移引擎")
	_, err = db.ExecContext(ctx, `INSERT INTO client_device_workers(device_id,worker_id,ssh_host_key_fingerprint)
 SELECT device_id,worker_id,ssh_host_key_fingerprint FROM client_device_workers LIMIT 1`)
	require.Error(t, err, "新写入不能通过数据库默认值猜测引擎")
}
