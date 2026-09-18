package workerconfig

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestRunChannelNegotiatesWakeAndReceivesNotification(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	notified := make(chan []string, 1)
	ready := make(chan bool, 1)
	handlerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		conn, err := upgrader.Upgrade(response, request, nil)
		if err != nil {
			return
		}
		defer close(handlerDone)
		defer func() { _ = conn.Close() }()
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var hello workerprotocol.WorkerRPCRequest
		if json.Unmarshal(payload, &hello) != nil {
			return
		}
		require.Equal(t, workerprotocol.MessageTypeRequest, hello.Type)
		require.Equal(t, "hello", hello.Method)
		var params workerprotocol.WorkerHelloRequest
		require.NoError(t, json.Unmarshal(hello.Params, &params))
		require.Contains(t, params.Capabilities, workerprotocol.WakeCapability)
		ack, _ := json.Marshal(workerprotocol.WorkerRPCResponse{
			Type: workerprotocol.MessageTypeResponse, ID: hello.ID,
			Result: workerprotocol.WorkerHelloResponse{
				Capabilities: []string{workerprotocol.WakeCapability}},
		})
		if conn.WriteMessage(websocket.TextMessage, ack) != nil {
			return
		}
		notification, _ := json.Marshal(workerprotocol.WorkerNotification{
			Type: workerprotocol.MessageTypeNotify, Kinds: []string{workerprotocol.WakeClaim}})
		if conn.WriteMessage(websocket.TextMessage, notification) != nil {
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		_ = RunChannel(ctx, ChannelOptions{
			ControlURL: server.URL, Credential: "credential",
			ProtocolVersion: workerprotocol.Version,
			Notify:          func(kinds []string) { notified <- kinds },
			Ready:           func(wake bool) { ready <- wake },
		})
	}()
	t.Cleanup(func() {
		cancel()
		wait.Wait()
	})

	select {
	case wake := <-ready:
		require.True(t, wake)
	case <-time.After(5 * time.Second):
		t.Fatal("控制通道未完成 hello 协商")
	}
	select {
	case kinds := <-notified:
		require.Equal(t, []string{workerprotocol.WakeClaim}, kinds)
	case <-time.After(5 * time.Second):
		t.Fatal("控制通道未收到唤醒通知")
	}
}

func TestRunChannelKeepsServingLegacyRPCRequests(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	handled := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		conn, err := upgrader.Upgrade(response, request, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _, err = conn.ReadMessage()
		if err != nil {
			return
		}
		// 旧版 Control 不回应 hello，直接下发配置 RPC。
		payload, _ := json.Marshal(workerprotocol.WorkerRPCRequest{
			ID: "legacy-1", Method: "unsupported.method"})
		if conn.WriteMessage(websocket.TextMessage, payload) != nil {
			return
		}
		_, reply, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var parsed workerprotocol.WorkerRPCResponse
		if json.Unmarshal(reply, &parsed) == nil {
			handled <- parsed.ID
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		_ = RunChannel(ctx, ChannelOptions{
			ControlURL: server.URL, Credential: "credential",
			ProtocolVersion: workerprotocol.Version,
		})
	}()
	t.Cleanup(func() {
		cancel()
		wait.Wait()
	})

	select {
	case id := <-handled:
		require.Equal(t, "legacy-1", id)
	case <-time.After(5 * time.Second):
		t.Fatal("控制通道未响应旧版 RPC 请求")
	}
}
