package workerprotocol

import (
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientCredentialConcurrentRequestsAndEngineSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, []string{"Bearer test-before", "Bearer test-after"}, r.Header.Get("Authorization"))
		assert.Contains(t, []string{"codex", "claude-code"}, r.Header.Get(EngineHeader))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := NewClient(server.URL, "test-before", time.Second)
	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				client.SetCredential("test-before")
				client.SetCredential("test-after")
				runtime.Gosched()
			}
		}
	})
	var readers sync.WaitGroup
	for operation := range 3 {
		readers.Go(func() {
			for range 30 {
				switch operation {
				case 0:
					assert.NoError(t, client.Heartbeat(t.Context(), HeartbeatRequest{}))
				case 1:
					_, _, err := client.DownloadAttachment(t.Context(), &Task{}, uuid.New(), io.Discard)
					assert.NoError(t, err)
				case 2:
					clone, err := client.ForEngine(runtimeidentity.Claude)
					if !assert.NoError(t, err) {
						return
					}
					assert.NoError(t, clone.Heartbeat(t.Context(), HeartbeatRequest{}))
				}
			}
		})
	}
	readers.Wait()
	close(stop)
	writer.Wait()
	client.SetCredential("test-before")
	clone, err := client.ForEngine(runtimeidentity.Claude)
	require.NoError(t, err)
	client.SetCredential("test-after")
	require.Equal(t, "test-before", clone.credentialValue(), "引擎客户端保留独立凭据快照")
	clone.SetCredential("test-before")
	require.Equal(t, "test-after", client.credentialValue())
	client.SetCredential("")
	require.ErrorContains(t, client.Heartbeat(t.Context(), HeartbeatRequest{}), "Worker尚未注册")
	_, _, err = client.DownloadAttachment(t.Context(), &Task{}, uuid.New(), io.Discard)
	require.ErrorContains(t, err, "Worker尚未注册")
	require.NoError(t, clone.Heartbeat(t.Context(), HeartbeatRequest{}))
}
