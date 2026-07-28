package replygate

import (
	"os"
	"path/filepath"
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

func TestInstallRemovesLegacyGlobalHook(t *testing.T) {
	home := t.TempDir()
	config := `model = "mock-model"

# BEGIN TYRS HAND REPLY HOOK
[[hooks.Stop]]
[[hooks.Stop.hooks]]
type = "command"
command = "tyrs-hand-reply-hook"
# END TYRS HAND REPLY HOOK
`
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(home, "hooks.json"), []byte("{}\n"), 0o600))
	require.NoError(t, Install(home))
	updated, err := os.ReadFile(filepath.Join(home, "config.toml"))
	require.NoError(t, err)
	require.Contains(t, string(updated), `model = "mock-model"`)
	require.NotContains(t, string(updated), "TYRS HAND REPLY HOOK")
	require.NotContains(t, string(updated), HookCommand)
	_, err = os.Stat(filepath.Join(home, "hooks.json"))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, Install(home))
}

func TestSessionConfigUsesSessionFlagsTrustKey(t *testing.T) {
	config := SessionConfig()
	hooks := config["hooks"].(map[string]any)
	state := hooks["state"].(map[string]any)
	trust := state[sessionFlagsConfigPath+":stop:0:0"].(map[string]any)
	require.Equal(t, hookTrustedHash(), trust["trusted_hash"])
	require.Len(t, hooks["Stop"].([]any), 1)
}
