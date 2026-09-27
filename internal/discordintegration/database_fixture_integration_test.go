//go:build integration

package discordintegration

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/stretchr/testify/require"
)

// CI 已启动并验证 PostgreSQL；只复用服务，每个用例仍使用独立数据库。
func openDiscordCIDatabase(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	address, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"postgres", "postgresql"}, address.Scheme)
	admin, err := database.Open(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, admin.Close()) })
	name := "discord_fixture_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.ExecContext(ctx, "CREATE DATABASE "+pq.QuoteIdentifier(name))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, dropErr := admin.ExecContext(cleanupCtx, "DROP DATABASE "+pq.QuoteIdentifier(name))
		require.NoError(t, dropErr)
	})
	address.Path, address.RawPath = "/"+name, ""
	query := address.Query()
	query.Del("dbname")
	address.RawQuery = query.Encode()
	db, err := database.Open(ctx, address.String())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func TestDiscordDatabaseUsesIsolatedCIDatabases(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("该回归验证 CI 显式提供的 PostgreSQL 服务")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	admin, err := database.Open(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, admin.Close()) })
	var parentName, parentServer string
	require.NoError(t, admin.QueryRowContext(ctx,
		"SELECT current_database(),system_identifier::text FROM pg_control_system()").
		Scan(&parentName, &parentServer))
	var names []string
	t.Run("隔离且不清空已有数据", func(t *testing.T) {
		first, second := discordDatabase(t), discordDatabase(t)
		for _, db := range []*sql.DB{first, second} {
			var name, server string
			require.NoError(t, db.QueryRowContext(ctx,
				"SELECT current_database(),system_identifier::text FROM pg_control_system()").
				Scan(&name, &server))
			require.Equal(t, parentServer, server, "必须复用 CI 已就绪的服务")
			require.NotEqual(t, parentName, name, "不得在 CI 共享库运行迁移或写业务数据")
			names = append(names, name)
		}
		require.NotEqual(t, names[0], names[1])
		_, err := first.ExecContext(ctx, "CREATE TABLE fixture_sentinel(value integer); INSERT INTO fixture_sentinel VALUES (7)")
		require.NoError(t, err)
		var absent bool
		require.NoError(t, second.QueryRowContext(ctx, "SELECT to_regclass('fixture_sentinel') IS NULL").Scan(&absent))
		require.True(t, absent, "后一用例不能读到前一用例的数据")
		var value int
		require.NoError(t, first.QueryRowContext(ctx, "SELECT value FROM fixture_sentinel").Scan(&value))
		require.Equal(t, 7, value, "隔离不能删除前一用例的数据")
	})
	require.Len(t, names, 2)
	for _, name := range names {
		var remaining int
		require.NoError(t, admin.QueryRowContext(ctx, "SELECT count(*) FROM pg_database WHERE datname=$1", name).Scan(&remaining))
		require.Zero(t, remaining, "子测试退出必须删除本轮隔离库")
	}
}
