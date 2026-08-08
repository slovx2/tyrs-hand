package sshtransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/slovx2/tyrs-hand/internal/hostworker"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
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

func TestAppServerUpgradeDropsUnsupportedNativeCompressionOffer(t *testing.T) {
	header := "GET /token HTTP/1.1\r\nHost: 127.0.0.1:1234\r\n" +
		"Origin: http://127.0.0.1:1234\r\nConnection: Upgrade\r\n" +
		"Upgrade: websocket\r\nSec-WebSocket-Key: dGVzdA==\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Extensions: permessage-deflate\r\n" +
		"User-Agent: okhttp/4.12.0\r\n\r\n"

	filtered := string(appServerUpgradeHeader([]byte(header)))
	require.NotContains(t, strings.ToLower(filtered), "sec-websocket-extensions")
	require.Contains(t, filtered, "Origin: http://127.0.0.1:1234\r\n")
	require.Contains(t, filtered, "User-Agent: okhttp/4.12.0\r\n")
	require.True(t, strings.HasSuffix(filtered, "\r\n\r\n"))
}

func TestLoopbackRelaysWorkerSSHProxy(t *testing.T) {
	var key keyDescription
	encodedKey, err := GenerateEd25519Key()
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(encodedKey), &key))
	signer, err := parseSigner(key.PrivateKey, "")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	server, err := hostworker.StartSSHServer(ctx, hostworker.SSHOptions{
		ListenAddr: "127.0.0.1:0", HostKeyFile: filepath.Join(t.TempDir(), "host_key"),
		Home: t.TempDir(), CodexHome: t.TempDir(), Shell: "/bin/sh",
		AuthorizedClients: []hostworker.AuthorizedClient{{
			ID: "mobile", PublicKey: signer.PublicKey(),
		}},
		Runtime: mobileWebSocketRuntime{}, Logger: zap.NewNop(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	host, rawPort, err := net.SplitHostPort(server.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(rawPort)
	require.NoError(t, err)
	fingerprint, err := ProbeHost(host, port, "mobile")
	require.NoError(t, err)

	encodedEndpoint, err := OpenAppServer("worker-profile", host, port, "mobile",
		key.PrivateKey, "", fingerprint)
	require.NoError(t, err)
	t.Cleanup(func() { Close("worker-profile") })
	var endpoint appServerEndpoint
	require.NoError(t, json.Unmarshal([]byte(encodedEndpoint), &endpoint))

	connection, response, err := websocket.DefaultDialer.Dial(endpoint.URL, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	messageType, message, err := connection.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, websocket.TextMessage, messageType)
	require.Equal(t, "ready", string(message))
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

type mobileWebSocketRuntime struct{}

func (mobileWebSocketRuntime) ServeDesktop(connection net.Conn) error {
	done := make(chan struct{})
	tracked := &mobileSignalConnection{Conn: connection, done: done}
	listener := &mobileSingleConnectionListener{connection: tracked, done: done}
	server := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		ws, err := upgrader.Upgrade(response, request, nil)
		if err == nil {
			_ = ws.WriteMessage(websocket.TextMessage, []byte("ready"))
			_ = ws.Close()
		}
	})}
	err := server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

type mobileSingleConnectionListener struct {
	connection net.Conn
	done       chan struct{}
	once       sync.Once
}

func (l *mobileSingleConnectionListener) Accept() (net.Conn, error) {
	var connection net.Conn
	l.once.Do(func() { connection = l.connection })
	if connection != nil {
		return connection, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *mobileSingleConnectionListener) Close() error   { return l.connection.Close() }
func (l *mobileSingleConnectionListener) Addr() net.Addr { return l.connection.LocalAddr() }

type mobileSignalConnection struct {
	net.Conn
	done chan struct{}
	once sync.Once
}

func (c *mobileSignalConnection) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}
