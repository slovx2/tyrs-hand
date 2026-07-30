package main

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestServiceProxyForwardsOnlyToLoopbackPort(t *testing.T) {
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = target.Close() }()
	go func() {
		connection, acceptErr := target.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		_, _ = io.Copy(connection, connection)
	}()

	ctx, cancel := context.WithCancel(context.Background())
	socketDir, err := os.MkdirTemp("/tmp", "tyrs-service-proxy-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "proxy.sock")
	done := make(chan error, 1)
	go func() { done <- serveServiceProxy(ctx, socketPath) }()
	require.Eventually(t, func() bool {
		connection, dialErr := net.DialTimeout("unix", socketPath, 20*time.Millisecond)
		if dialErr != nil {
			return false
		}
		_ = connection.Close()
		return true
	}, time.Second, 10*time.Millisecond)

	connection, err := net.Dial("unix", socketPath)
	require.NoError(t, err)
	defer func() { _ = connection.Close() }()
	header := make([]byte, 6)
	copy(header, serviceProxyMagic)
	binary.BigEndian.PutUint16(header[4:], uint16(target.Addr().(*net.TCPAddr).Port))
	_, err = connection.Write(header)
	require.NoError(t, err)
	status := make([]byte, 3)
	_, err = io.ReadFull(connection, status)
	require.NoError(t, err)
	require.Equal(t, byte(0), status[0])
	_, err = connection.Write([]byte("loopback"))
	require.NoError(t, err)
	echo := make([]byte, len("loopback"))
	_, err = io.ReadFull(connection, echo)
	require.NoError(t, err)
	require.Equal(t, "loopback", string(echo))

	cancel()
	require.NoError(t, <-done)
}

func TestServiceProxyRejectsInvalidHeader(t *testing.T) {
	server, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		handleServiceProxyConnection(server)
		close(done)
	}()
	_, err := client.Write([]byte("NOPE\x00\x01"))
	require.NoError(t, err)
	status := make([]byte, 3)
	_, err = io.ReadFull(client, status)
	require.NoError(t, err)
	require.Equal(t, byte(1), status[0])
	_ = client.Close()
	<-done
}
