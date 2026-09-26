package mockcodex

import (
	"encoding/json"
	"testing"

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
