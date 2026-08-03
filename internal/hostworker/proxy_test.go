package hostworker

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBrowserAgentProxyRegistersAndForwardsBothDirections(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()

	client, proxyStream := net.Pipe()
	defer func() { _ = client.Close() }()
	errCh := make(chan error, 1)
	go func() {
		errCh <- BrowserAgentProxy(listener.Addr().String(), "v1.workspace.signature")(
			context.Background(), proxyStream)
	}()

	bridge, err := listener.Accept()
	require.NoError(t, err)
	defer func() { _ = bridge.Close() }()
	require.Equal(t, map[string]string{
		"type": "register", "token": "v1.workspace.signature",
	}, readBrowserAgentFrame(t, bridge))

	bridgePayload := []byte("TYRS-BROWSER/2\nbridge-frame")
	go func() { _, _ = bridge.Write(bridgePayload) }()
	forwarded := make([]byte, len(bridgePayload))
	_, err = io.ReadFull(client, forwarded)
	require.NoError(t, err)
	require.Equal(t, bridgePayload, forwarded)

	agentPayload := []byte("agent-frame")
	go func() { _, _ = client.Write(agentPayload) }()
	forwarded = make([]byte, len(agentPayload))
	_, err = io.ReadFull(bridge, forwarded)
	require.NoError(t, err)
	require.Equal(t, agentPayload, forwarded)

	require.NoError(t, client.Close())
	select {
	case <-errCh:
	case <-time.After(3 * time.Second):
		t.Fatal("Browser Agent Proxy 未在客户端断线后退出")
	}
}

func TestBrowserAgentProxyConnectionsAreIsolated(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	proxy := BrowserAgentProxy(listener.Addr().String(), "v1.workspace.signature")

	firstClient, firstProxy := net.Pipe()
	secondClient, secondProxy := net.Pipe()
	defer func() { _ = secondClient.Close() }()
	go func() { _ = proxy(context.Background(), firstProxy) }()
	firstBridge, err := listener.Accept()
	require.NoError(t, err)
	_ = readBrowserAgentFrame(t, firstBridge)
	go func() { _ = proxy(context.Background(), secondProxy) }()
	secondBridge, err := listener.Accept()
	require.NoError(t, err)
	defer func() { _ = secondBridge.Close() }()
	_ = readBrowserAgentFrame(t, secondBridge)

	require.NoError(t, firstClient.Close())
	require.NoError(t, firstBridge.Close())
	payload := []byte("second-still-connected")
	go func() { _, _ = secondBridge.Write(payload) }()
	forwarded := make([]byte, len(payload))
	_, err = io.ReadFull(secondClient, forwarded)
	require.NoError(t, err)
	require.Equal(t, payload, forwarded)
}

func readBrowserAgentFrame(t *testing.T, reader io.Reader) map[string]string {
	t.Helper()
	var size uint32
	require.NoError(t, binary.Read(reader, binary.BigEndian, &size))
	require.LessOrEqual(t, size, uint32(browserAgentRegistrationLimit))
	payload := make([]byte, size)
	_, err := io.ReadFull(reader, payload)
	require.NoError(t, err)
	var message map[string]string
	require.NoError(t, json.Unmarshal(payload, &message))
	return message
}
