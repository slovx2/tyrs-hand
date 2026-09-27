package mockcodex

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestThreadResponsesKeepTurnSnapshot(t *testing.T) {
	tests := []struct {
		name string
		read func(*testing.T, *Server, string) Thread
	}{
		{"read", func(t *testing.T, server *Server, id string) Thread {
			thread, ok := server.thread(id)
			require.True(t, ok)
			return thread
		}},
		{"list", func(t *testing.T, server *Server, _ string) Thread {
			threads := server.listThreads(false)
			require.Len(t, threads, 1)
			return threads[0]
		}},
		{"settings", func(t *testing.T, server *Server, id string) Thread {
			thread, ok := server.updateThreadSettings(id, "on-request", ":workspace", "model", "high", "fast")
			require.True(t, ok)
			return thread
		}},
		{"runtime", func(_ *testing.T, server *Server, id string) Thread {
			return server.updateThreadRuntime(id, "model", "high", "fast")
		}},
		{"name", func(t *testing.T, server *Server, id string) Thread {
			thread, ok := server.updateThreadName(id, "name")
			require.True(t, ok)
			return thread
		}},
		{"unarchive", func(t *testing.T, server *Server, id string) Thread {
			thread, ok := server.setThreadArchived(id, false)
			require.True(t, ok)
			return thread
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{threads: make(map[string]Thread)}
			thread := server.createThread(t.TempDir(), false)
			turn, err := server.startTurn(thread.ID, "message")
			require.NoError(t, err)
			snapshot := test.read(t, server, thread.ID)
			require.Len(t, snapshot.Turns, 1)
			require.Equal(t, "inProgress", snapshot.Turns[0].Status)

			// 响应出锁后可以延迟序列化，后续完成回合不能改写已取得的响应。
			_, ok := server.finishTurn(thread.ID, turn.ID, "completed")
			require.True(t, ok)
			payload, err := json.Marshal(threadResponse(snapshot))
			require.NoError(t, err)
			var response struct {
				Thread Thread `json:"thread"`
			}
			require.NoError(t, json.Unmarshal(payload, &response))
			require.Equal(t, "inProgress", response.Thread.Turns[0].Status)

			current, ok := server.thread(thread.ID)
			require.True(t, ok)
			require.Equal(t, "completed", current.Turns[0].Status)
		})
	}
}

// 原生 app-server 的响应与紧随的 thread/started 同队列输出；外部 Emit 不能插到二者之间，
// 否则测试方在拿到创建响应后注入的事件会早于 thread/started 到达。
func TestEmitCannotInterleaveResponseAndFollowUpNotification(t *testing.T) {
	server, err := Start(t)
	require.NoError(t, err)
	dialer := websocket.Dialer{NetDialContext: DialContext(server.SocketPath)}
	ws, response, err := dialer.DialContext(t.Context(), "ws://localhost/", nil)
	require.NoError(t, err)
	_ = response.Body.Close()
	t.Cleanup(func() { _ = ws.Close() })
	read := func() Message {
		t.Helper()
		var message Message
		require.NoError(t, ws.ReadJSON(&message))
		return message
	}
	require.NoError(t, ws.WriteJSON(Message{ID: json.RawMessage("1"), Method: "initialize"}))
	require.Equal(t, json.RawMessage("1"), read().ID)

	responded, release := make(chan struct{}), make(chan struct{})
	hook := func() {
		close(responded)
		<-release
	}
	server.afterRespond.Store(&hook)
	params, err := json.Marshal(map[string]string{"cwd": t.TempDir()})
	require.NoError(t, err)
	require.NoError(t, ws.WriteJSON(Message{ID: json.RawMessage("2"), Method: "thread/start", Params: params}))
	<-responded
	server.afterRespond.Store(nil)
	created := read()
	require.Equal(t, json.RawMessage("2"), created.ID)
	var result struct{ Thread struct{ ID string } }
	require.NoError(t, json.Unmarshal(created.Result, &result))

	emitted := make(chan struct{})
	go func() {
		server.Emit(result.Thread.ID, "thread/deleted", map[string]string{"threadId": result.Thread.ID})
		close(emitted)
	}()
	select {
	case <-emitted:
		t.Fatal("外部通知插入到创建响应与 thread/started 之间")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-emitted
	require.Equal(t, "thread/started", read().Method)
	require.Equal(t, "thread/deleted", read().Method)
}
