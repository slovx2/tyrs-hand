//go:build integration

package database

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestParticipantIdentityMigrationBindsExistingSSHToEnvironmentOwner(t *testing.T) {
	ctx := context.Background()
	db := migrationTestDatabase(t)
	_, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (
		version text PRIMARY KEY,
		checksum char(64) NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now())`)
	require.NoError(t, err)
	migrations, err := loadMigrations()
	require.NoError(t, err)
	connection, err := db.Conn(ctx)
	require.NoError(t, err)
	for _, item := range migrations {
		if item.version >= "020_" {
			break
		}
		if item.nonTx {
			require.NoError(t, applyNonTransactional(ctx, connection, item))
		} else {
			require.NoError(t, applyTransactional(ctx, connection, item))
		}
	}
	require.NoError(t, connection.Close())

	_, err = db.ExecContext(ctx, `INSERT INTO discord_guilds(guild_id, enabled)
		VALUES ('100000000000000001', true)`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO discord_members
		(guild_id, discord_user_id, username, display_name)
		VALUES ('100000000000000001','100000000000000002','owner','Owner')`)
	require.NoError(t, err)
	var installationID, repositoryID, environmentID, nodeID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO scm_installations
		(provider, external_id, account_login, account_type)
		VALUES ('github',9001,'owner','Organization') RETURNING id`).Scan(&installationID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO repositories
		(installation_id, provider, external_id, owner, name, default_branch, clone_url)
		VALUES ($1,'github',9002,'owner','repo','main','https://example.invalid/repo.git')
		RETURNING id`, installationID).Scan(&repositoryID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO execution_nodes
		(name, roles, credential_hash, max_concurrent_jobs, protocol_version)
		VALUES ('migration-node','["discord"]'::jsonb,'hash',1,2) RETURNING id`,
	).Scan(&nodeID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_development_environments
		(guild_id, owner_discord_user_id, build_repository_id, container_name,
		 data_volume_name, home_volume_name, network_name, execution_node_id,
		 ssh_public_key, ssh_fingerprint, ssh_port)
		VALUES ('100000000000000001','100000000000000002',$1,'migration-env',
		 'migration-data','migration-home','migration-network',$2,
		 'ssh-ed25519 existing','SHA256:existing',2222) RETURNING id`,
		repositoryID, nodeID).Scan(&environmentID))

	require.NoError(t, Migrate(ctx, db))
	var publicKey, fingerprint, ownerID string
	var port, protocolVersion int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT ssh_public_key, ssh_fingerprint,
		ssh_port, ssh_discord_user_id FROM discord_development_environments WHERE id=$1`,
		environmentID).Scan(&publicKey, &fingerprint, &port, &ownerID))
	require.Equal(t, "ssh-ed25519 existing", publicKey)
	require.Equal(t, "SHA256:existing", fingerprint)
	require.Equal(t, 2222, port)
	require.Equal(t, "100000000000000002", ownerID)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT protocol_version FROM execution_nodes
		WHERE id=$1`, nodeID).Scan(&protocolVersion))
	require.Equal(t, 3, protocolVersion)
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
	require.NoError(t, err)
	db, err := Open(ctx, fmt.Sprintf(
		"postgres://tyrs_hand:test-password@%s:%s/tyrs_hand?sslmode=disable",
		host, port.Port()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}
