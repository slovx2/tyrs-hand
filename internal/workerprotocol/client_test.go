package workerprotocol

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClientUsesDirectWorkerV1Entrypoints(t *testing.T) {
	paths := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		paths <- request.URL.Path
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
