package codexproxy

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestForwardCopiesDesktopBytesWithoutInterpretingWebSocket(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "tyrs-stdio-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })
	socketPath := filepath.Join(directory, "app-server.sock")
	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	upstream := make(chan []byte, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		payload, _ := io.ReadAll(connection)
		upstream <- payload
		_, _ = connection.Write([]byte("HTTP/1.1 101 Switching Protocols\r\n\r\n\x81\x02ok"))
	}()

	request := []byte("GET / HTTP/1.1\r\nUpgrade: websocket\r\n\r\n\x81\x03raw")
	var output bytes.Buffer
	require.NoError(t, Forward(context.Background(), socketPath, bytes.NewReader(request), &output))
	require.Equal(t, request, <-upstream)
	require.Equal(t, "HTTP/1.1 101 Switching Protocols\r\n\r\n\x81\x02ok", output.String())
}
