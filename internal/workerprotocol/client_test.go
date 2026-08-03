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

func TestClientUsesOnlyWorkerV2Entrypoints(t *testing.T) {
	paths := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		paths <- request.URL.Path
		switch request.URL.Path {
		case "/worker/v2/enroll":
			_ = json.NewEncoder(response).Encode(EnrollResponse{ProtocolVersion: Version})
		case "/worker/v2/sync":
			var envelope RequestEnvelope
			require.NoError(t, json.NewDecoder(request.Body).Decode(&envelope))
			require.NotEmpty(t, envelope.RequestID)
			require.NotZero(t, envelope.Sequence)
			require.Empty(t, envelope.Parameters)
			if envelope.Operation == "worker.claim" {
				_ = json.NewEncoder(response).Encode(ClaimResponse{})
			} else {
				response.WriteHeader(http.StatusNoContent)
			}
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	client := NewClient(server.URL, "credential", time.Second)
	_, err := client.Enroll(context.Background(), "token")
	require.NoError(t, err)
	require.NoError(t, client.Heartbeat(context.Background(), HeartbeatRequest{}))
	_, err = client.Claim(context.Background(), ClaimRequest{WorkerID: "worker", Role: "all"})
	require.NoError(t, err)
	close(paths)
	for path := range paths {
		require.NotContains(t, path, "/worker/v1/")
	}
}

func TestOperationEnvelopeDoesNotExposePrivateRoutes(t *testing.T) {
	runID := "3cf17f7b-a8d0-4cce-9eb8-f3b96ef12b25"
	operation, parameters, err := ResolveOperation(http.MethodPost,
		"/worker/v1/runs/"+runID+"/events")
	require.NoError(t, err)
	require.Equal(t, "run.events.append", operation)
	require.Equal(t, map[string]string{"id": runID}, parameters)

	method, path, err := ResolveOperationRoute(operation, parameters)
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, method)
	require.Equal(t, "/worker/v1/runs/"+runID+"/events", path)
}
