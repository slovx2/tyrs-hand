package sshtransport

import (
	"bytes"
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoopbackAcceptsOnlyExactOneTimeUpgradePath(t *testing.T) {
	valid := "GET /random-token HTTP/1.1\r\nHost: 127.0.0.1\r\n" +
		"Connection: keep-alive, Upgrade\r\nUpgrade: websocket\r\n" +
		"Sec-WebSocket-Key: dGVzdA==\r\nSec-WebSocket-Version: 13\r\n\r\n"
	raw, ok := validateUpgrade(t, valid, "/random-token")
	require.True(t, ok)
	require.Equal(t, valid, string(raw))

	_, ok = validateUpgrade(t, valid, "/other-token")
	require.False(t, ok)
}

func TestProxyHandshakeMustBeWebSocketUpgrade(t *testing.T) {
	valid := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Connection: Upgrade\r\nUpgrade: websocket\r\n" +
		"Sec-WebSocket-Accept: dGVzdA==\r\n\r\n"
	header, err := readProxyUpgrade(bytes.NewBufferString(valid + "first-frame"))
	require.NoError(t, err)
	require.Equal(t, valid, string(header))

	_, err = readProxyUpgrade(bytes.NewBufferString(
		"HTTP/1.1 503 Service Unavailable\r\nContent-Length: 0\r\n\r\n"))
	require.ErrorContains(t, err, "拒绝 Upgrade")
}

func validateUpgrade(t *testing.T, request, expectedPath string) ([]byte, bool) {
	t.Helper()
	server, client := net.Pipe()
	go func(connection net.Conn) {
		_, _ = io.WriteString(connection, request)
		_ = connection.Close()
	}(client)
	raw, ok := readLegalUpgrade(server, expectedPath)
	require.NoError(t, server.Close())
	return raw, ok
}
