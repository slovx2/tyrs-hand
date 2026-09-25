package appserverhub

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimeInfoUsesFixedHostIdentity(t *testing.T) {
	calls := 0
	hub := &Hub{options: Options{RuntimeInfo: func() any {
		calls++
		return map[string]string{"engine": "codex", "workerId": "one-worker"}
	}}}
	result, err := hub.routeCall(context.Background(), nil, "runtime/info", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"engine":"codex","workerId":"one-worker"}`, string(result))
	for _, input := range []string{`null`, `[]`, `{"engine":"claude-code"}`, `{"threadId":"other"}`} {
		_, err := hub.routeCall(context.Background(), nil, "runtime/info", json.RawMessage(input))
		var protocolErr *ProtocolError
		require.ErrorAs(t, err, &protocolErr)
		require.Equal(t, -32602, protocolErr.Code)
	}
	require.Equal(t, 1, calls)
}
