package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDesktopTurnInputUsesDelegatedUserTextForProjection(t *testing.T) {
	threadID, instruction, err := desktopTurnInput(json.RawMessage(`{
		"threadId":"desktop-thread",
		"input":[{
			"type":"text",
			"text":"<codex_delegation>\n<source_thread_id>source-thread</source_thread_id>\n<input>运行 git status &amp;&amp; 只回复结果</input>\n</codex_delegation>"
		}]
	}`))

	require.NoError(t, err)
	require.Equal(t, "desktop-thread", threadID)
	require.Equal(t, "运行 git status && 只回复结果", instruction)
}

func TestDesktopTurnInputPreservesOrdinaryAndMalformedText(t *testing.T) {
	for name, input := range map[string]string{
		"ordinary":  "请检查 <input> 标签",
		"malformed": "<codex_delegation><input>不要截断",
		"extra":     "前缀 <codex_delegation><source_thread_id>x</source_thread_id><input>正文</input></codex_delegation>",
	} {
		t.Run(name, func(t *testing.T) {
			params, err := json.Marshal(map[string]any{
				"threadId": "desktop-thread",
				"input": []map[string]string{{
					"type": "text",
					"text": input,
				}},
			})
			require.NoError(t, err)

			_, instruction, err := desktopTurnInput(params)

			require.NoError(t, err)
			require.Equal(t, input, instruction)
		})
	}
}
