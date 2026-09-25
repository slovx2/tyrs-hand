package workerprotocol

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

func TestClientUsesDirectWorkerV1Entrypoints(t *testing.T) {
	paths := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		paths <- request.URL.Path
		if request.URL.Path != "/worker/v1/enroll" {
			require.Equal(t, strconv.Itoa(Version), request.Header.Get(VersionHeader))
		}
		switch request.URL.Path {
		case "/worker/v1/enroll":
			_ = json.NewEncoder(response).Encode(EnrollResponse{ProtocolVersion: Version})
		case "/worker/v1/heartbeat":
			response.WriteHeader(http.StatusNoContent)
		case "/worker/v1/claims":
			_ = json.NewEncoder(response).Encode(ClaimResponse{})
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	client := NewClient(server.URL, "credential", time.Second)
	_, err := client.Enroll(context.Background(), "token")
	require.NoError(t, err)
	require.NoError(t, client.Heartbeat(context.Background(), HeartbeatRequest{}))
	_, err = client.Claim(context.Background(), ClaimRequest{Role: "all"})
	require.NoError(t, err)
	close(paths)
	for path := range paths {
		require.Contains(t, path, "/worker/v1/")
		require.NotContains(t, path, "/worker/v2/")
	}
}

func TestClientRuntimeScopeCannotBeOverriddenByPayload(t *testing.T) {
	engines := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		engines <- request.Header.Get(EngineHeader)
		require.Equal(t, "Bearer credential", request.Header.Get("Authorization"))
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{}`))
	}))
	defer server.Close()
	base := NewClient(server.URL, "credential", time.Second)
	claude, err := base.ForEngine(runtimeidentity.Claude)
	require.NoError(t, err)
	_, err = base.ForEngine("gpt")
	require.Error(t, err)
	payload := DesktopThreadPrepareRequest{Params: json.RawMessage(`{"engine":"codex","model":"gpt-test","threadId":"same-thread"}`)}
	_, err = claude.PrepareDesktopThread(t.Context(), payload)
	require.NoError(t, err)
	require.Equal(t, "claude-code", <-engines)
	_, err = base.PrepareDesktopThread(t.Context(), payload)
	require.NoError(t, err)
	require.Equal(t, "codex", <-engines, "创建 Claude 客户端不应改变原客户端")
	_, err = claude.PrepareDesktopThread(t.Context(), payload)
	require.NoError(t, err)
	require.Equal(t, "claude-code", <-engines)
}
