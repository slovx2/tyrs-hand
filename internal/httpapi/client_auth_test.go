package httpapi

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClientBearerCredentialsPreferAuthorizationHeader(t *testing.T) {
	request := httptest.NewRequest("GET", "/api/v1/client/bootstrap", nil)
	request.Header.Set("Authorization", "Bearer header-token")
	request.Header.Set("Sec-WebSocket-Protocol", "tyrs-hand.bearer.socket-token")

	token, protocol := clientBearerCredentials(request)

	require.Equal(t, "header-token", token)
	require.Empty(t, protocol)
}

func TestClientBearerCredentialsAcceptWebSocketSubprotocol(t *testing.T) {
	request := httptest.NewRequest("GET", "/api/v1/client/updates", nil)
	request.Header.Set("Sec-WebSocket-Protocol",
		"unrelated, tyrs-hand.bearer.socket-token")

	token, protocol := clientBearerCredentials(request)

	require.Equal(t, "socket-token", token)
	require.Equal(t, "tyrs-hand.bearer.socket-token", protocol)
}
