package httpapi

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestLiveDelegationEventTypes(t *testing.T) {
	require.True(t, isLiveDelegationEvent("session.delegation.created"))
	require.True(t, isLiveDelegationEvent("delegation.created"))
	require.True(t, isLiveDelegationEvent("conversation.handoff.requested"))
	require.False(t, isLiveDelegationEvent("session.input_transcript.done"))
}

func TestLiveDelegationID(t *testing.T) {
	require.Equal(t, "handoff_abc", liveDelegationID(map[string]any{
		"type": "conversation.handoff.requested", "handoff_id": "handoff_abc",
	}))
	require.Equal(t, "del_1", liveDelegationID(map[string]any{
		"type":       "session.delegation.created",
		"delegation": map[string]any{"id": "del_1", "target": "client"},
	}))
	require.Equal(t, "handoff_abc", liveDelegationID(map[string]any{
		"type": "delegation.created",
		"item": map[string]any{"id": "item_1", "handoff_id": "handoff_abc", "target": "client"},
	}))
}

func TestStripLiveVoicePrefix(t *testing.T) {
	channel, text := stripLiveVoicePrefix("[STATUS] 测试还在跑")
	require.Equal(t, "[STATUS]", channel)
	require.Equal(t, "测试还在跑", text)
	channel, text = stripLiveVoicePrefix("[COMPLETE] 做完了")
	require.Equal(t, "[COMPLETE]", channel)
	require.Equal(t, "做完了", text)
	channel, text = stripLiveVoicePrefix("普通回答")
	require.Equal(t, "", channel)
	require.Equal(t, "普通回答", text)
}

func TestIsLiveVoiceTool(t *testing.T) {
	require.True(t, isLiveVoiceTool("list_sessions"))
	require.True(t, isLiveVoiceTool("transfer_voice_call"))
	require.False(t, isLiveVoiceTool("automation_update"))
	require.False(t, isLiveVoiceTool("capture_screen_context"))
}

func TestTruncateLiveCommentary(t *testing.T) {
	require.Equal(t, "短文本", truncateLiveCommentary("短文本"))
	long := strings.Repeat("测", liveCommentaryLimit+8)
	got := truncateLiveCommentary(long)
	require.Equal(t, liveCommentaryLimit, utf8.RuneCountInString(got))
}

func TestForwardLiveVoiceTextIgnoresUnprefixed(t *testing.T) {
	channel, text := stripLiveVoicePrefix("[ANALYSIS] 内部推理")
	require.Equal(t, "[ANALYSIS]", channel)
	require.Equal(t, "内部推理", text)
	channel, text = stripLiveVoicePrefix("  [COMMENTARY] 还在跑  ")
	require.Equal(t, "[COMMENTARY]", channel)
	require.Equal(t, "还在跑", text)
}

func TestLiveVoiceWritebackPayload(t *testing.T) {
	payload := liveVoiceWritebackPayload("测试还在跑", "[STATUS]", "handoff_1")
	require.Equal(t, "session.context.append", payload["type"])
	require.Equal(t, "speakable", payload["channel"])
	content, ok := payload["content"].([]map[string]any)
	require.True(t, ok)
	require.Equal(t, "input_text", content[0]["type"])
	require.Equal(t, "测试还在跑", content[0]["text"])
	quiet := liveVoiceWritebackPayload("内部推理", "[ANALYSIS]", "")
	require.Equal(t, "session.context.append", quiet["type"])
	_, hasChannel := quiet["channel"]
	require.False(t, hasChannel)
	_, hasID := quiet["id"]
	require.False(t, hasID)
}

func TestDefaultLiveInstructionsCopiesChatGPTPolicies(t *testing.T) {
	require.Contains(t, defaultLiveInstructions, "You are Tyrs Hand's voice receptionist")
	require.Contains(t, defaultLiveInstructions, "Pass execution work to the backend")
	require.Contains(t, defaultLiveInstructions, "NEVER refuse requests")
	require.Contains(t, defaultLiveInstructions, "Do not read out or recreate tables, diffs, plots, code blocks")
	require.Contains(t, defaultLiveInstructions, "Do not invent Live event types")
	require.Contains(t, defaultLiveInstructions, "stop speaking, or pause is not a request to hang up")
	require.Contains(t, defaultLiveInstructions, "Do not mention anything about backend")
	require.NotContains(t, defaultLiveInstructions, "capture_screen_context")
	require.NotContains(t, defaultLiveInstructions, "{{ user_first_name }}")
	require.NotContains(t, defaultLiveInstructions, "[USER]")
	require.NotContains(t, defaultLiveInstructions, "[BACKEND]")
	require.NotContains(t, defaultLiveInstructions, "delegation.created")
}
