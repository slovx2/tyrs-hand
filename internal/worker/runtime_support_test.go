package worker

import (
	"strings"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestApplyLiveVoiceSessionSupportCoordinator(t *testing.T) {
	instructions, tools := applyLiveVoiceSessionSupport(&workerprotocol.SessionSnapshot{
		VoiceBound: true, VoiceCoordinator: true,
	}, "base", nil)
	require.True(t, strings.HasPrefix(instructions, "base\n\n"))
	require.Contains(t, instructions, "You are coordinating a voice chat")
	require.Contains(t, instructions, "Choose one of three modes")
	require.Contains(t, instructions, `"What should we do today?" Stay here.`)
	require.Contains(t, instructions, "[STATUS]")
	require.Contains(t, instructions, "tyrs_hand.list_sessions")
	require.Contains(t, instructions, "tyrs_hand.end_voice_call")
	require.NotContains(t, instructions, "capture_screen_context")
	require.NotContains(t, instructions, "wait_threads")
	require.NotContains(t, instructions, "create_thread")
	require.NotContains(t, instructions, "Uber Eats")
	require.Equal(t, "tyrs_hand", tools[0].Name)
	require.True(t, hasVoiceTool(tools, "list_sessions"))
	require.True(t, hasVoiceTool(tools, "transfer_voice_call"))
	require.False(t, hasVoiceTool(tools, "capture_screen_context"))
}

func TestApplyLiveVoiceSessionSupportExistingTask(t *testing.T) {
	instructions, tools := applyLiveVoiceSessionSupport(&workerprotocol.SessionSnapshot{
		VoiceBound: true,
	}, "keep going", nil)
	require.Contains(t, instructions, "Realtime voice is active for this existing Codex task")
	require.Contains(t, instructions, "these tools are deferred")
	require.Contains(t, instructions, "stop speaking")
	require.Contains(t, instructions, "[COMPLETE]")
	require.NotContains(t, instructions, "You are coordinating a voice chat")
	require.True(t, hasVoiceTool(tools, "end_voice_call"))
}

func TestApplyLiveVoiceSessionSupportEnded(t *testing.T) {
	instructions, tools := applyLiveVoiceSessionSupport(&workerprotocol.SessionSnapshot{
		VoiceEnded: true,
	}, "base", nil)
	require.Contains(t, instructions, "Realtime voice mode has ended")
	require.Contains(t, instructions, "Do not add realtime channel prefixes")
	require.NotContains(t, instructions, "[STATUS]")
	require.Empty(t, tools)
}

func TestApplyLiveVoiceSessionSupportUnrelated(t *testing.T) {
	existing := []ports.DynamicToolSpec{{Type: "function", Name: "generate_image"}}
	instructions, tools := applyLiveVoiceSessionSupport(&workerprotocol.SessionSnapshot{},
		"base", existing)
	require.Equal(t, "base", instructions)
	require.Equal(t, existing, tools)
}

func hasVoiceTool(tools []ports.DynamicToolSpec, name string) bool {
	for _, spec := range tools {
		if spec.Type == "namespace" && spec.Name == "tyrs_hand" {
			for _, tool := range spec.Tools {
				if tool.Name == name {
					return true
				}
			}
		}
	}
	return false
}
