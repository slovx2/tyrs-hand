package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestAppServerTunnelReservationsAreSingleUse(t *testing.T) {
	broker := newAppServerTunnelBroker()
	workerID := uuid.New()
	ticket, issued, err := broker.issue(workerID)
	require.NoError(t, err)
	t.Cleanup(func() { broker.finish(issued) })

	claimContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	claimed, err := broker.claim(claimContext, workerID)
	require.NoError(t, err)
	require.Same(t, issued, claimed)

	mobile, err := broker.mobile(ticket)
	require.NoError(t, err)
	require.Same(t, issued, mobile)
	_, err = broker.mobile(ticket)
	require.Error(t, err)

	worker, err := broker.worker(issued.id, workerID)
	require.NoError(t, err)
	require.Same(t, issued, worker)
	_, err = broker.worker(issued.id, workerID)
	require.Error(t, err)
	_, err = broker.worker(issued.id, uuid.New())
	require.Error(t, err)
}

func TestRelayWebSocketMessagesPreservesFramesAndOrder(t *testing.T) {
	leftClient, leftServer := websocketPair(t)
	rightClient, rightServer := websocketPair(t)
	finished := make(chan struct{})
	go func() {
		relayWebSocketMessages(leftServer, rightServer)
		close(finished)
	}()
	t.Cleanup(func() {
		_ = leftClient.Close()
		_ = rightClient.Close()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("等待 WebSocket 隧道关闭超时")
		}
	})

	frames := []struct {
		messageType int
		payload     string
	}{
		{websocket.TextMessage, `{"id":2,"result":{"turnId":"turn-1"}}`},
		{websocket.TextMessage, `{"method":"turn/started","params":{"turn":{"id":"turn-1"}}}`},
		{websocket.BinaryMessage, "\x00\x01opaque"},
	}
	for _, frame := range frames {
		require.NoError(t, leftClient.WriteMessage(frame.messageType, []byte(frame.payload)))
	}
	for _, expected := range frames {
		messageType, payload, err := rightClient.ReadMessage()
		require.NoError(t, err)
		require.Equal(t, expected.messageType, messageType)
		require.Equal(t, []byte(expected.payload), payload)
	}

	require.NoError(t, rightClient.WriteMessage(websocket.TextMessage,
		[]byte(`{"method":"item/completed","params":{"item":{"id":"item-1"}}}`)))
	messageType, payload, err := leftClient.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, websocket.TextMessage, messageType)
	require.JSONEq(t, `{"method":"item/completed","params":{"item":{"id":"item-1"}}}`, string(payload))
}

func websocketPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		connection, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).
			Upgrade(response, request, nil)
		if err != nil {
			return
		}
		accepted <- connection
	}))
	t.Cleanup(server.Close)
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http")
	client, response, err := websocket.DefaultDialer.Dial(endpoint, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	require.NoError(t, err)
	select {
	case remote := <-accepted:
		return client, remote
	case <-time.After(time.Second):
		require.FailNow(t, "WebSocket 测试连接超时")
		return nil, nil
	}
}
