//go:build integration

package database

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestFreshHostWorkerBaseline(t *testing.T) {
	ctx := context.Background()
	db := migrationTestDatabase(t)
	require.NoError(t, Migrate(ctx, db))
	require.NoError(t, CheckMigrations(ctx, db))

	for _, table := range []string{
		"workers", "worker_enrollments", "worker_workspaces", "workspace_projects",
		"workspace_sessions", "ssh_host_workers", "github_agent_repository_overrides",
	} {
		var exists bool
		require.NoError(t, db.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM information_schema.tables
			WHERE table_schema='public' AND table_name=$1)`, table).Scan(&exists))
		require.True(t, exists, table)
	}

	var legacyCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*)
		FROM information_schema.columns
		WHERE table_schema='public' AND (
			table_name IN ('execution_nodes','worker_nodes','discord_development_environments',
				'development_projects','development_sessions','discord_development_operations',
				'codex_auth_operations','codex_runtime_settings')
			OR column_name IN ('container_id','image_ref','data_volume_name','home_volume_name',
				'network_name','runtime_uid','runtime_gid','codex_home_key','execution_node_id',
				'development_workspace_id','development_project_id'))`).Scan(&legacyCount))
	require.Zero(t, legacyCount)
}

func migrationTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "postgres:18.3-bookworm@sha256:80630f83606d8db77d30b3851b16a9f78be2d0d4dda6f7b82a1fdca5ebe3acba",
			Env: map[string]string{
				"POSTGRES_DB": "tyrs_hand", "POSTGRES_USER": "tyrs_hand",
				"POSTGRES_PASSWORD": "test-password",
			},
			ExposedPorts: []string{"5432/tcp"},
			WaitingFor: wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, testcontainers.TerminateContainer(container)) })
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	for attempt := 0; err != nil && attempt < 50; attempt++ {
		time.Sleep(100 * time.Millisecond)
		port, err = container.MappedPort(ctx, "5432/tcp")
	}
	require.NoError(t, err)
	db, err := Open(ctx, fmt.Sprintf(
		"postgres://tyrs_hand:test-password@%s:%s/tyrs_hand?sslmode=disable",
		host, port.Port()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}
