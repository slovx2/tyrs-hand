package worker

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func unwrapWorkerTestRequest(t *testing.T, request *http.Request) {
	t.Helper()
	if request.URL.Path != "/worker/v2/rpc" && request.URL.Path != "/worker/v2/sync" {
		return
	}
	var envelope workerprotocol.RequestEnvelope
	require.NoError(t, json.NewDecoder(request.Body).Decode(&envelope))
	require.NotEmpty(t, envelope.RequestID)
	require.NotZero(t, envelope.Sequence)
	method, path, err := workerprotocol.ResolveOperationRoute(envelope.Operation,
		envelope.Parameters)
	require.NoError(t, err)
	request.Method = method
	request.URL.Path = path
	request.Body = io.NopCloser(bytes.NewReader(envelope.Payload))
}
