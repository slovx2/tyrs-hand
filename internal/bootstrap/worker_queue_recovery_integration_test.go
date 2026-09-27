//go:build integration

package bootstrap

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

type bootstrapQueueRecoveryGate struct {
	active  atomic.Bool
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

// 重启后通过真实 CLI 写入未加载线程，模拟磁盘上已有的分页队列；不写内部数据库。
func verifyBootstrapQueueRecovery(t *testing.T, ctx context.Context, app *WorkerApp, signer ssh.Signer,
	clients map[runtimeidentity.Engine]*codex.SocketClient, connectionsClosed map[runtimeidentity.Engine]<-chan struct{},
	threads map[runtimeidentity.Engine]string, gate *bootstrapQueueRecoveryGate, codexCalls *atomic.Int64,
) {
	t.Helper()
	entry, err := app.Runtimes.Entry(runtimeidentity.Codex)
	require.NoError(t, err)
	previousGeneration := entry.Runtime.Generation()
	require.NoError(t, entry.Runtime.Restart())
	require.NotEqual(t, previousGeneration, entry.Runtime.Generation())
	client, closed := connectBootstrapSSH(t, ctx, entry, signer)
	clients[runtimeidentity.Codex], connectionsClosed[runtimeidentity.Codex] = client, closed
	threadID := threads[runtimeidentity.Codex]
	modelCalls := codexCalls.Load()
	ids := make([]string, 0, 52)
	for index := 0; index < 52; index++ {
		var result struct {
			Submission struct {
				ID string `json:"id"`
			} `json:"queuedSubmission"`
		}
		require.NoError(t, entry.Runtime.Client().Call(ctx, "thread/queue/add", map[string]any{
			"threadId": threadID, "clientUserMessageId": uuid.NewString(),
			"input": []map[string]any{{"type": "text", "text": fmt.Sprintf("persisted queue item %d", index), "text_elements": []any{}}},
		}, &result))
		require.NotEmpty(t, result.Submission.ID)
		ids = append(ids, result.Submission.ID)
	}
	claudeEvents := clients[runtimeidentity.Claude].Subscribe(codex.ThreadFilter{ThreadID: threads[runtimeidentity.Claude]})
	t.Cleanup(claudeEvents.Close)
	startClaude := func() error {
		var result any
		return clients[runtimeidentity.Claude].Call(ctx, "turn/start", map[string]any{
			"threadId": threads[runtimeidentity.Claude],
			"input":    []map[string]any{{"type": "text", "text": "queue recovery capacity barrier", "text_elements": []any{}}},
		}, &result)
	}
	gate.active.Store(true)
	require.Eventually(t, func() bool { return startClaude() == nil }, 3*time.Second, 20*time.Millisecond)
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, method := range []string{"thread/resume", "thread/queue/start"} {
		var response any
		err := client.Call(ctx, method, map[string]any{"threadId": threadID}, &response)
		require.ErrorContains(t, err, "并发上限", "恢复入口必须在加载或启动持久队列前预留额度")
	}
	require.Equal(t, modelCalls, codexCalls.Load(), "并发拒绝必须先于真实原生执行")
	gate.active.Store(false)
	close(gate.release)
	awaitBootstrapTurn(t, ctx, claudeEvents)
	// 官方队列最多一百项；读取每页五十项，第二页仍必须占用槽。
	for index, id := range ids {
		var response struct {
			Deleted bool `json:"deleted"`
		}
		deleteItem := func() error {
			return client.Call(ctx, "thread/queue/delete", map[string]any{"threadId": threadID, "queuedSubmissionId": id}, &response)
		}
		if index == 0 {
			require.Eventually(t, func() bool { return deleteItem() == nil }, 3*time.Second, 20*time.Millisecond)
		} else {
			require.NoError(t, deleteItem())
		}
		require.True(t, response.Deleted)
		if index == 49 {
			require.ErrorContains(t, startClaude(), "并发上限", "不能遗漏第 51 和 52 条恢复队列")
		}
	}
	require.Eventually(t, func() bool { return startClaude() == nil }, 3*time.Second, 20*time.Millisecond)
	awaitBootstrapTurn(t, ctx, claudeEvents)
	var resumed any
	require.NoError(t, client.Call(ctx, "thread/resume", map[string]any{"threadId": threadID}, &resumed))
	require.Equal(t, modelCalls, codexCalls.Load(), "全部删除的恢复项不得被重放")
	saveBootstrapArtifact(t, "queue-recovery", runtimeidentity.Codex, map[string]any{
		"persistedItems": len(ids), "generationChanged": true, "resumeRejectedAtCapacity": true,
		"startRejectedAtCapacity": true, "secondPageRetainedCapacity": true, "deletedItemsModelCalls": 0,
	})
}
