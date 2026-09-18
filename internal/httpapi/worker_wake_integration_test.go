//go:build integration

package httpapi

import (
	"context"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

// TestWorkerWakeResolverQueriesMatchSchema 校验唤醒解析 SQL 与实际数据库结构一致。
// 空库上返回空结果，但列名或表名写错会直接报错。
func TestWorkerWakeResolverQueriesMatchSchema(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, _ := workerTestServer(t, db)

	for _, kind := range []string{workerprotocol.WakeClaim, workerprotocol.WakeSessionTitle,
		workerprotocol.WakeThreadSync} {
		targets, err := server.resolveWorkerWakeTargets(ctx, kind)
		require.NoError(t, err, "kind=%s", kind)
		require.Empty(t, targets, "kind=%s", kind)
	}
}
