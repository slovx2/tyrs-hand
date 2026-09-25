package appserverhub

import (
	"encoding/json"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

func TestConnectionResourcesSeparateSameIDsAndEvents(t *testing.T) {
	for _, scenario := range []struct{ start, event, field string }{
		{"fs/watch", "fs/changed", "watchId"},
		{"process/spawn", "process/outputDelta", "processHandle"},
		{"command/exec", "command/exec/outputDelta", "processId"},
	} {
		t.Run(scenario.start, func(t *testing.T) {
			var received [2][]rpcMessage
			hub := &Hub{sessions: make(map[int64]*session)}
			var encoded [2]json.RawMessage
			var finishes [2]func(error)
			for index := range 2 {
				s := newSession(int64(index+1), RoleDesktop, func(message rpcMessage) error {
					received[index] = append(received[index], message)
					return nil
				}, nil)
				hub.sessions[s.id] = s
				params, err := json.Marshal(map[string]any{scenario.field: "same:id/中文"})
				require.NoError(t, err)
				encoded[index], finishes[index], err = hub.scopeResourceCall(s, scenario.start, params)
				require.NoError(t, err)
				_, _, err = hub.scopeResourceCall(s, scenario.start, params)
				require.Error(t, err, "同连接重复活动 ID 必须拒绝")
			}
			require.NotEqual(t, string(encoded[0]), string(encoded[1]))
			for index := range 2 {
				require.True(t, hub.forwardResourceEvent(codex.Event{Method: scenario.event, Params: encoded[index]}))
				require.Len(t, received[index], 1)
				var params map[string]any
				require.NoError(t, json.Unmarshal(received[index][0].Params, &params))
				require.Equal(t, "same:id/中文", params[scenario.field])
			}
			for index := range 2 {
				require.Len(t, received[index], 1)
			}
			// 无归属和已断开的事件都不能回落到广播。
			require.True(t, hub.forwardResourceEvent(codex.Event{Method: scenario.event, Params: json.RawMessage(`{}`)}))
			delete(hub.sessions, 1)
			require.True(t, hub.forwardResourceEvent(codex.Event{Method: scenario.event, Params: encoded[0]}))
			require.Len(t, received[1], 1)
			for _, finish := range finishes {
				finish(&ProtocolError{Code: -32602, Message: "失败"})
			}
			require.Empty(t, hub.resources, "失败创建不能留下 ID 占用")
		})
	}
}
