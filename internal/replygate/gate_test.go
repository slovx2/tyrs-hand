package replygate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReplyGateBlocksThreeTimesThenFailsOpen(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, Initialize(home, "thread-1", "intent-1", true, 3))
	for index := 0; index < 3; index++ {
		decision := Evaluate(home, "thread-1")
		require.True(t, decision.Block)
		require.Contains(t, decision.Reason, "reply_to_github")
	}
	require.False(t, Evaluate(home, "thread-1").Block)
	state, err := Read(home, "thread-1")
	require.NoError(t, err)
	require.Equal(t, 4, state.BlockCount)
}

func TestReplyGateAllowsDeliveredBypassAndBrokenState(t *testing.T) {
	home := t.TempDir()
	for _, test := range []struct {
		name  string
		state State
	}{{"silent", State{}}, {"delivered", State{Required: true, Delivered: true}},
		{"bypass", State{Required: true, Bypass: true}}} {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, Write(home, test.name, test.state))
			require.False(t, Evaluate(home, test.name).Block)
		})
	}
	require.False(t, Evaluate(home, "missing").Block)
	require.NoError(t, os.MkdirAll(filepath.Dir(Path(home, "broken")), 0o700))
	require.NoError(t, os.WriteFile(Path(home, "broken"), []byte("{"), 0o600))
	require.False(t, Evaluate(home, "broken").Block)
}

func TestInstallWritesHookAndTrust(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, Install(home))
	hooks, err := os.ReadFile(filepath.Join(home, "hooks.json"))
	require.NoError(t, err)
	require.Contains(t, string(hooks), HookCommand)
	config, err := os.ReadFile(filepath.Join(home, "config.toml"))
	require.NoError(t, err)
	require.Contains(t, string(config), "trusted_hash")
	require.NoError(t, Install(home))
	config, err = os.ReadFile(filepath.Join(home, "config.toml"))
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(config), "# BEGIN TYRS HAND REPLY HOOK"))
}

func TestSessionConfigUsesSessionFlagsTrustKey(t *testing.T) {
	config := SessionConfig()
	hooks := config["hooks"].(map[string]any)
	state := hooks["state"].(map[string]any)
	trust := state[sessionFlagsConfigPath+":stop:0:0"].(map[string]any)
	require.Equal(t, hookTrustedHash(), trust["trusted_hash"])
	require.Len(t, hooks["Stop"].([]any), 1)
}
