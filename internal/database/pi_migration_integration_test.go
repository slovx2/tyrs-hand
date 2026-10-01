//go:build integration

package database

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPiMigrationPreservesRuntimeConstraints(t *testing.T) {
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
		if strings.HasPrefix(item.version, "037_") {
			break
		}
		require.NoError(t, applyTransactional(ctx, conn, item))
	}
	require.NoError(t, conn.Close())
	_, err = db.ExecContext(ctx, `
INSERT INTO administrators(username,password_hash,totp_secret_ciphertext) VALUES ('pi-test','hash','');
INSERT INTO workers(name) VALUES ('pi-test');
INSERT INTO client_devices(id,administrator_id,name,platform,credential_hash)
SELECT gen_random_uuid(),id,'test phone','android','test-only' FROM administrators;
INSERT INTO client_device_workers(device_id,worker_id,engine,ssh_host_key_fingerprint)
SELECT device.id,worker.id,'codex','SHA256:'||repeat('a',43) FROM client_devices device,workers worker;`)
	require.NoError(t, err)
	var beforeFK, beforeTriggers int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM pg_constraint WHERE contype='f'`).Scan(&beforeFK))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM pg_trigger WHERE NOT tgisinternal`).Scan(&beforeTriggers))
	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, Migrate(ctx, db))
	var afterFK, afterTriggers int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM pg_constraint WHERE contype='f'`).Scan(&afterFK))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM pg_trigger WHERE NOT tgisinternal`).Scan(&afterTriggers))
	require.Equal(t, beforeFK, afterFK)
	require.Equal(t, beforeTriggers, afterTriggers)
	_, err = db.ExecContext(ctx, `INSERT INTO client_device_workers(device_id,worker_id,engine,ssh_host_key_fingerprint)
SELECT device_id,worker_id,'pi','SHA256:'||repeat('b',43) FROM client_device_workers WHERE engine='codex'`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE client_device_workers SET engine='claude-code' WHERE engine='pi'`)
	require.Error(t, err, "Pi 授权的引擎身份不可变")
	_, err = db.ExecContext(ctx, `INSERT INTO client_device_workers(device_id,worker_id,engine,ssh_host_key_fingerprint)
SELECT device_id,worker_id,'unknown',ssh_host_key_fingerprint FROM client_device_workers LIMIT 1`)
	require.Error(t, err, "新增 Pi 不得接受未知引擎")
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM pg_constraint
WHERE contype='c' AND conname IN ('worker_runtimes_engine_check','workspace_sessions_engine_check',
'codex_thread_controls_engine_check','desktop_thread_requests_engine_check','scheduled_tasks_engine_check',
'client_device_workers_engine_check','client_device_pairings_engine_check','discord_forums_default_engine_check',
'discord_conversations_engine_check','discord_user_codex_preferences_engine_check')
AND pg_get_constraintdef(oid) LIKE '%pi%'`).Scan(&count))
	require.Equal(t, 10, count)
}
