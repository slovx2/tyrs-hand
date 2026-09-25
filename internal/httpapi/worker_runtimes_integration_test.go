//go:build integration

package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/auth"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/slovx2/tyrs-hand/internal/workerregistry"
	"github.com/stretchr/testify/require"
)

func testRuntimeReport(engine runtimeidentity.Engine, fingerprint string) workerprotocol.RuntimeReport {
	address := ":2222"
	if engine == runtimeidentity.Claude {
		address = ":3333"
	}
	return workerprotocol.RuntimeReport{
		Engine: engine, Status: "running", SSHListenAddress: address,
		SSHHostKeyFingerprint: fingerprint, ProtocolVersion: "0.147.0",
		Build: workerprotocol.RuntimeBuild{CLIBuild: "test-cli"}, Capabilities: []string{},
		ModelCatalog: json.RawMessage(`{"data":[]}`),
	}
}

func testCodexRuntimeReports(fingerprint string) []workerprotocol.RuntimeReport {
	return []workerprotocol.RuntimeReport{testRuntimeReport(runtimeidentity.Codex, fingerprint)}
}

func TestWorkerRuntimeHeartbeatIsolationAndAtomicity(t *testing.T) {
	db := workerDatabase(t)
	ctx := t.Context()
	require.NoError(t, database.Migrate(ctx, db))
	server, endpoint := workerTestServer(t, db)
	worker, token, err := server.workers.Create(ctx, "dual-runtime", []string{"discord"}, 2)
	require.NoError(t, err)
	_, credential, err := server.workers.Enroll(ctx, token)
	require.NoError(t, err)
	client := workerprotocol.NewClient(endpoint, credential, 5*time.Second)
	// 协调升级期间，数据库期望版本已更新也不能让旧进程领取或操作任务。
	for _, version := range []string{"", "32", "34"} {
		for _, path := range []string{"/worker/v1/claims", "/worker/v1/config/ws"} {
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+path, strings.NewReader(`{"role":"discord"}`))
			require.NoError(t, err)
			if strings.HasSuffix(path, "/ws") {
				request.Method = http.MethodGet
			}
			request.Header.Set("Authorization", "Bearer "+credential)
			request.Header.Set(workerprotocol.VersionHeader, version)
			response, err := http.DefaultClient.Do(request)
			require.NoError(t, err)
			require.Equal(t, http.StatusConflict, response.StatusCode)
			require.NoError(t, response.Body.Close())
		}
	}
	codexKey, claudeKey := testWorkerFingerprint(worker.ID), testWorkerFingerprint(uuid.New())
	report := workerprotocol.HeartbeatRequest{WorkerVersion: "test", ProtocolVersion: workerprotocol.Version,
		SSHHostKeyFingerprint: codexKey, Runtimes: []workerprotocol.RuntimeReport{
			testRuntimeReport(runtimeidentity.Codex, codexKey), testRuntimeReport(runtimeidentity.Claude, claudeKey),
		}}
	report.Runtimes[0].ModelCatalog = json.RawMessage(`{"data":[{"id":"gpt-test"}]}`)
	report.Runtimes[1].ModelCatalog = json.RawMessage(`{"data":[{"id":"claude-test"}]}`)
	require.NoError(t, client.Heartbeat(ctx, report))
	read := func() map[runtimeidentity.Engine]workerregistry.Runtime {
		rows, err := server.workers.Runtimes(ctx, worker.ID)
		require.NoError(t, err)
		result := map[runtimeidentity.Engine]workerregistry.Runtime{}
		for _, row := range rows {
			result[row.Engine] = row
		}
		return result
	}
	stored := read()
	require.Len(t, stored, 2)
	require.Equal(t, claudeKey, stored[runtimeidentity.Claude].SSHHostKeyFingerprint)
	require.JSONEq(t, string(report.Runtimes[0].ModelCatalog), string(stored[runtimeidentity.Codex].ModelCatalog))
	require.JSONEq(t, string(report.Runtimes[1].ModelCatalog), string(stored[runtimeidentity.Claude].ModelCatalog))
	for _, role := range []string{"admin", "user"} {
		response := httptest.NewRecorder()
		request, _ := gin.CreateTestContext(response)
		request.Request = httptest.NewRequest("GET", "/api/v1/workers/"+worker.ID.String()+"/runtimes", nil)
		request.Params = gin.Params{{Key: "id", Value: worker.ID.String()}}
		request.Set("session", auth.Session{Role: role, AdministratorID: uuid.New()})
		server.getWorkerRuntimes(request)
		if role == "admin" {
			require.Equal(t, 200, response.Code)
			var visible []workerregistry.Runtime
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &visible))
			require.Len(t, visible, 2)
		} else {
			require.Equal(t, 403, response.Code, "运行时信息仍须通过 Worker 授权检查")
		}
	}

	// 单引擎故障保持另一引擎在线，完整快照可显式清空某引擎的目录。
	report.Runtimes[1].Status = "unavailable"
	report.Runtimes[1].ModelCatalog = nil
	require.NoError(t, client.Heartbeat(ctx, report))
	stored = read()
	require.Equal(t, "running", stored[runtimeidentity.Codex].Status)
	require.Equal(t, "unavailable", stored[runtimeidentity.Claude].Status)
	require.JSONEq(t, "null", string(stored[runtimeidentity.Claude].ModelCatalog))

	// 指纹变更必须回滚整个快照，包括已经更新的 Codex 状态。
	report.Runtimes[0].Status = "stopped"
	report.Runtimes[1].SSHHostKeyFingerprint = testWorkerFingerprint(uuid.New())
	require.Error(t, client.Heartbeat(ctx, report))
	require.Equal(t, "running", read()[runtimeidentity.Codex].Status)
	report.Runtimes[1].SSHHostKeyFingerprint = claudeKey
	report.Runtimes[0].Status = "running"

	// 停用 Claude 保留它的身份；重新启用必须继续使用原 Host Key。
	claude := report.Runtimes[1]
	report.Runtimes = report.Runtimes[:1]
	require.NoError(t, client.Heartbeat(ctx, report))
	stored = read()
	require.False(t, stored[runtimeidentity.Claude].Enabled)
	require.Equal(t, "disabled", stored[runtimeidentity.Claude].Status)
	require.Equal(t, claudeKey, stored[runtimeidentity.Claude].SSHHostKeyFingerprint)
	report.Runtimes = append(report.Runtimes, claude)
	require.NoError(t, client.Heartbeat(ctx, report))
	require.True(t, read()[runtimeidentity.Claude].Enabled)

	// 另一台 Worker 不能抢占任一入口指纹；失败不留下部分登记。
	other, token, err := server.workers.Create(ctx, "other-runtime", []string{"discord"}, 2)
	require.NoError(t, err)
	_, credential, err = server.workers.Enroll(ctx, token)
	require.NoError(t, err)
	otherClient := workerprotocol.NewClient(endpoint, credential, 5*time.Second)
	otherKey := testWorkerFingerprint(other.ID)
	otherReport := workerprotocol.HeartbeatRequest{WorkerVersion: "test", ProtocolVersion: workerprotocol.Version,
		SSHHostKeyFingerprint: otherKey, Runtimes: []workerprotocol.RuntimeReport{
			testRuntimeReport(runtimeidentity.Codex, otherKey), testRuntimeReport(runtimeidentity.Claude, claudeKey),
		}}
	require.Error(t, otherClient.Heartbeat(ctx, otherReport))
	otherRows, err := server.workers.Runtimes(ctx, other.ID)
	require.NoError(t, err)
	require.Empty(t, otherRows)

	_, err = db.ExecContext(ctx, `UPDATE worker_runtimes SET heartbeat_at=now()-interval '3 minutes' WHERE worker_id=$1`, worker.ID)
	require.NoError(t, err)
	require.Equal(t, "offline", read()[runtimeidentity.Codex].Status)
	require.Equal(t, "offline", read()[runtimeidentity.Claude].Status)

	report.Runtimes = nil
	require.Error(t, client.Heartbeat(ctx, report), "禁止缺失运行时的旧心跳伪装成功")
}
