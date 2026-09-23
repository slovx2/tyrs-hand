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
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestCallWorkerRPCRenewsExpiredWriteDeadline(t *testing.T) {
	connected := make(chan *websocket.Conn, 1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err == nil {
			connected <- conn
		}
	}))
	defer httpServer.Close()

	workerConn, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(httpServer.URL, "http"), nil)
	require.NoError(t, err)
	defer workerConn.Close()
	serverConn := <-connected
	defer serverConn.Close()

	require.NoError(t, serverConn.SetWriteDeadline(time.Now().Add(-time.Second)))
	workerID := uuid.New()
	state := &workerRPCConnection{
		workerID: workerID, conn: serverConn,
		pending: make(map[string]chan workerprotocol.WorkerRPCResponse),
	}
	server := &Server{workerRPCConns: map[uuid.UUID]*workerRPCConnection{workerID: state}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type outcome struct {
		result any
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := server.callWorkerRPC(ctx, workerID, "config.read", nil, time.Second)
		done <- outcome{result: result, err: err}
	}()

	require.NoError(t, workerConn.SetReadDeadline(time.Now().Add(2*time.Second)))
	var request workerprotocol.WorkerRPCRequest
	require.NoError(t, workerConn.ReadJSON(&request))
	require.Equal(t, "config.read", request.Method)
	state.mu.Lock()
	wait := state.pending[request.ID]
	state.mu.Unlock()
	require.NotNil(t, wait)
	wait <- workerprotocol.WorkerRPCResponse{ID: request.ID, Result: "ok"}

	response := <-done
	require.NoError(t, response.err)
	require.Equal(t, "ok", response.result)
}
