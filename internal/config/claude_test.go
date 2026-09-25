package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaudeDefaultsAndPortValidation(t *testing.T) {
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, ":2222", cfg.WorkerSSHListenAddr)
	require.Equal(t, ":3333", cfg.WorkerClaudeSSHListenAddr)
	require.False(t, cfg.WorkerClaudeEnabled)
	cfg.WorkerClaudeEnabled = true
	require.NoError(t, cfg.validateClaudeRuntime())
	for _, address := range []string{":2222", "localhost:2222", ":0", "", ":99999"} {
		cfg.WorkerClaudeSSHListenAddr = address
		require.Error(t, cfg.validateClaudeRuntime(), address)
	}
	cfg.WorkerClaudeSSHListenAddr = ":3333"
	cfg.WorkerClaudeBin = "claude"
	require.Error(t, cfg.validateClaudeRuntime())
}
