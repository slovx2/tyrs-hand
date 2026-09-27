//go:build integration

package bootstrap

import (
	"context"
	"database/sql"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/stretchr/testify/require"
)

// 只复用临时 PostgreSQL 服务，不复用业务数据；全局后台任务和 Outbox 不按 Worker 过滤。
func openControlRuntimeDatabase(t *testing.T, ctx context.Context, dsn string) *sql.DB {
	t.Helper()
	address, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"postgres", "postgresql"}, address.Scheme)
	admin, err := database.Open(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, admin.Close()) })
	name := "control_fixture_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.ExecContext(ctx, "CREATE DATABASE "+pq.QuoteIdentifier(name))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, dropErr := admin.ExecContext(cleanupCtx, "DROP DATABASE "+pq.QuoteIdentifier(name))
		require.NoError(t, dropErr)
	})
	address.Path = "/" + name
	address.RawPath = ""
	db, err := database.Open(ctx, address.String())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func TestControlRuntimeFixturesIsolateDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ready := make(chan struct{})
	close(ready)
	first := newControlRuntimeFixture(t, ctx, "http://127.0.0.1:1", ready)
	require.NoError(t, discordintegration.NewSQLoutbox(first.db).Enqueue(ctx,
		"fixture-isolation-sentinel", "message.create", "channels/fixture/messages",
		map[string]string{"channelId": "fixture", "content": "前一专项未投递消息"}, ""))
	second := newControlRuntimeFixture(t, ctx, "http://127.0.0.1:1", ready)
	var pending int
	require.NoError(t, second.db.QueryRowContext(ctx,
		"SELECT count(*) FROM integration_outbox WHERE operation_key='fixture-isolation-sentinel'").Scan(&pending))
	require.Zero(t, pending, "后一专项不能领取前一专项的 Discord 消息")
	require.NoError(t, first.db.QueryRowContext(ctx,
		"SELECT count(*) FROM integration_outbox WHERE operation_key='fixture-isolation-sentinel'").Scan(&pending))
	require.Equal(t, 1, pending, "隔离不能靠清空前一专项的数据实现")
	var firstDatabase, secondDatabase string
	require.NoError(t, first.db.QueryRowContext(ctx, "SELECT current_database()").Scan(&firstDatabase))
	require.NoError(t, second.db.QueryRowContext(ctx, "SELECT current_database()").Scan(&secondDatabase))
	require.NotEqual(t, firstDatabase, secondDatabase)
}
