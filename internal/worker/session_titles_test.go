package worker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

type titleCaller struct {
	method string
	params map[string]any
}

func (c *titleCaller) Call(_ context.Context, method string, params, result any) error {
	c.method = method
	c.params = params.(map[string]any)
	var response any
	if method == "thread/start" {
		response = map[string]any{"thread": map[string]any{"id": "title-thread"}}
	} else {
		response = map[string]any{"turn": map[string]any{"id": "title-turn"}}
	}
	encoded, _ := json.Marshal(response)
	return json.Unmarshal(encoded, result)
}

func TestSessionTitleThreadIsEphemeralAndRestricted(t *testing.T) {
	caller := &titleCaller{}
	threadID, err := startSessionTitleThread(context.Background(), caller, t.TempDir())
	require.NoError(t, err)
	require.Equal(t, "title-thread", threadID)
	require.Equal(t, "thread/start", caller.method)
	require.Equal(t, sessionTitleModel, caller.params["model"])
	require.Equal(t, true, caller.params["ephemeral"])
	require.Equal(t, "never", caller.params["approvalPolicy"])
	require.Equal(t, "read-only", caller.params["sandbox"])
	require.Empty(t, caller.params["dynamicTools"])
	require.Equal(t, false, caller.params["config"].(map[string]any)["default_tools_enabled"])
}

func TestSessionTitleTurnUsesStructuredOutput(t *testing.T) {
	caller := &titleCaller{}
	turnID, err := startSessionTitleTurn(context.Background(), caller, "title-thread", "测试任务")
	require.NoError(t, err)
	require.Equal(t, "title-turn", turnID)
	require.Equal(t, "turn/start", caller.method)
	require.Equal(t, sessionTitleModel, caller.params["model"])
	require.Equal(t, "low", caller.params["effort"])
	schema := caller.params["outputSchema"].(map[string]any)
	require.Equal(t, "object", schema["type"])
	properties := schema["properties"].(map[string]any)
	require.Equal(t, sessionTitleMaxRunes,
		properties["title"].(map[string]any)["maxLength"])
}

func TestNormalizeGeneratedSessionTitle(t *testing.T) {
	require.Equal(t, "测试 标题", normalizeGeneratedSessionTitle("  测试\n标题  "))
	require.Len(t, []rune(normalizeGeneratedSessionTitle("一二三四五六七八九十一二三四五六七八九十"+
		"一二三四五六七八九十一二三四五六七八九十")), sessionTitleMaxRunes)
}
