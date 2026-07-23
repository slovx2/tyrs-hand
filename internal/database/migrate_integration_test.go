//go:build integration

package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
	require.Equal(t, 6, protocolVersion)
}

func TestDesktopTurnTerminalRepairMigration(t *testing.T) {
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
		if item.version >= "024_" {
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
	var installationID, repositoryID, profileID, environmentID uuid.UUID
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO scm_installations
		(provider, external_id, account_login, account_type)
		VALUES ('github',9101,'owner','Organization') RETURNING id`).Scan(&installationID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO repositories
		(installation_id, provider, external_id, owner, name, default_branch, clone_url)
		VALUES ($1,'github',9102,'owner','repo','main','https://example.invalid/repo.git')
		RETURNING id`, installationID).Scan(&repositoryID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO agent_profiles(name)
		VALUES ('Migration Desktop') RETURNING id`).Scan(&profileID))
	require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO discord_development_environments
		(guild_id, owner_discord_user_id, build_repository_id, container_name,
			data_volume_name, home_volume_name, network_name)
		VALUES ('100000000000000001','100000000000000002',$1,'migration-desktop-env',
			'migration-desktop-data','migration-desktop-home','migration-desktop-network')
		RETURNING id`, repositoryID).Scan(&environmentID))

	controlID, intentID, runID := uuid.New(), uuid.New(), uuid.New()
	_, err = db.ExecContext(ctx, `INSERT INTO codex_thread_controls
		(id, source_type, repository_id, agent_profile_id, context_version,
			development_environment_id, status, remote_status, active_client_id,
			worker_id, lease_token, lease_epoch, lease_expires_at, next_wakeup_at,
			last_error_code, last_error_message)
		VALUES ($1,'desktop_thread',$2,$3,1,$4,'reconciling','retry_wait',
			'desktop-client','desktop-relay',$5,1,now()+interval '1 minute',
			now()+interval '15 seconds','desktop_turn_error','Codex turn interrupted')`,
		controlID, repositoryID, profileID, environmentID, strings.Repeat("a", 64))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO codex_turn_intents
		(id, control_id, sequence_no, source_type, input_surface, agent_profile_id,
			idempotency_key, status, attempt_count, max_attempts,
			last_error_code, last_error_message)
		VALUES ($1,$2,1,'discord_conversation','desktop',$3,$4,'retry_wait',1,3,
			'desktop_turn_error','Codex turn interrupted')`,
		intentID, controlID, profileID, "migration-desktop-"+intentID.String())
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE codex_thread_controls
		SET active_intent_id=$2 WHERE id=$1`, controlID, intentID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO codex_turn_runs
		(id, control_id, primary_intent_id, attempt, worker_id, lease_epoch,
			capability_hash, status, error_code, error_message, finished_at)
		VALUES ($1,$2,$3,1,'desktop-relay',1,$4,'failed',
			'desktop_turn_error','Codex turn interrupted',now())`,
		runID, controlID, intentID, strings.Repeat("b", 64))
	require.NoError(t, err)

	require.NoError(t, Migrate(ctx, db))
	var controlStatus, remoteStatus string
	var activeIntent sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status, remote_status,
		active_intent_id::text FROM codex_thread_controls WHERE id=$1`, controlID).
		Scan(&controlStatus, &remoteStatus, &activeIntent))
	require.Equal(t, "idle", controlStatus)
	require.Equal(t, "idle", remoteStatus)
	require.False(t, activeIntent.Valid)
	var intentStatus, intentCode, runStatus, runCode string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status, last_error_code
		FROM codex_turn_intents WHERE id=$1`, intentID).Scan(&intentStatus, &intentCode))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status, error_code
		FROM codex_turn_runs WHERE id=$1`, runID).Scan(&runStatus, &runCode))
	require.Equal(t, "canceled", intentStatus)
	require.Equal(t, "user_interrupt", intentCode)
	require.Equal(t, "canceled", runStatus)
	require.Equal(t, "user_interrupt", runCode)
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
