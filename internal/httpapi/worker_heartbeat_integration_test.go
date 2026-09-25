//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

// TestWorkerHeartbeatMergesCatalogMetadata 验证心跳元数据按合并语义写入：
// 只带 revision 的心跳必须保留上一次上传的 modelCatalog，
// revision 为空则删除旧目录快照。
func TestWorkerHeartbeatMergesCatalogMetadata(t *testing.T) {
	db := workerDatabase(t)
	ctx := context.Background()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	worker, enrollment, err := server.workers.Create(ctx, "heartbeat-merge",
		[]string{"discord"}, 2)
	require.NoError(t, err)
	_, credential, err := server.workers.Enroll(ctx, enrollment)
	require.NoError(t, err)
	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)
	fingerprint := testWorkerFingerprint(worker.ID)
	catalog := `{"data":[{"id":"model-a"}]}`

	send := func(t *testing.T, metadata map[string]any) {
		t.Helper()
		encoded, err := json.Marshal(metadata)
		require.NoError(t, err)
		require.NoError(t, client.Heartbeat(ctx, workerprotocol.HeartbeatRequest{
			Runtimes:      testCodexRuntimeReports(fingerprint),
			WorkerVersion: "test", ProtocolVersion: workerprotocol.Version,
			SSHHostKeyFingerprint: fingerprint, Metadata: encoded}))
	}
	storedMetadata := func(t *testing.T) map[string]json.RawMessage {
		t.Helper()
		var stored []byte
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT metadata FROM workers WHERE id=$1`, worker.ID).Scan(&stored))
		var parsed map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(stored, &parsed))
		return parsed
	}

	send(t, map[string]any{"modelCatalog": json.RawMessage(catalog),
		"modelCatalogRevision": "rev-1"})
	require.JSONEq(t, catalog, string(storedMetadata(t)["modelCatalog"]))

	send(t, map[string]any{"modelCatalogRevision": "rev-1"})
	require.JSONEq(t, catalog, string(storedMetadata(t)["modelCatalog"]))

	send(t, map[string]any{"modelCatalogRevision": ""})
	require.NotContains(t, storedMetadata(t), "modelCatalog")
}
