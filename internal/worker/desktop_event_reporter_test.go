package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/codexcontrol"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestDesktopEventReporterBatchesEvents(t *testing.T) {
	var uploaded atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		_ *http.Request,
	) {
		uploaded.Add(1)
		response.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	processor := &Processor{cfg: config.Config{ControlTimeout: time.Second}, logger: zap.NewNop(),
		client: workerprotocol.NewClient(server.URL, "credential", time.Second)}
	task := &workerprotocol.Task{Snapshot: workerprotocol.TaskSnapshot{Runtime: workerprotocol.RuntimeSnapshot{Engine: "codex"}}, Claimed: codexcontrol.ClaimedControl{
		RunID: uuid.New(), Intent: codexcontrol.Intent{ID: uuid.New()}}}
	reporter, err := newDesktopEventReporter(ctx, processor, task)
	require.NoError(t, err)
	t.Cleanup(reporter.stopFlushLoop)

	for index := 0; index < 20; index++ {
		reporter.Report("turn/delta", json.RawMessage(`{"index":1}`))
	}
	// 首个事件立即上传，其余事件在去抖窗口内合并，因此请求数远小于事件数。
	require.LessOrEqual(t, uploaded.Load(), int64(2))

	require.Eventually(t, func() bool { return uploaded.Load() >= 2 },
		3*time.Second, 20*time.Millisecond)
}
