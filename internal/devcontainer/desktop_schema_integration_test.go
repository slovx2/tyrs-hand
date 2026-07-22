//go:build integration

package devcontainer

import (
	"context"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/stretchr/testify/require"
)

func TestDesktopBridgeSchemaAndPermanentEnvironmentLifecycle(t *testing.T) {
	ctx := context.Background()
	db := developmentDatabase(t)
	require.NoError(t, database.Migrate(ctx, db))

	for _, table := range []string{"desktop_thread_requests", "codex_interactive_requests"} {
		var exists bool
		require.NoError(t, db.QueryRowContext(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, table).Scan(&exists))
		require.True(t, exists, table)
	}
	for _, column := range []string{"ssh_public_key", "ssh_fingerprint", "ssh_port",
		"ssh_config_revision", "ssh_applied_revision", "daemon_status", "app_server_status",
		"ssh_daemon_status", "relay_status"} {
		var exists bool
		require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'discord_development_environments' AND column_name = $1)`, column).Scan(&exists))
		require.True(t, exists, column)
	}
	var idleColumn bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM information_schema.columns
		WHERE table_name = 'discord_development_environments' AND column_name = 'idle_at')`).Scan(&idleColumn))
	require.False(t, idleColumn)
}
