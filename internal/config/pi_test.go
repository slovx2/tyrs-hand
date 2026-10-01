package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPiRuntimeIndependentSwitchAndPort(t *testing.T) {
	cfg := Config{WorkerPiEnabled: true, WorkerPiBin: "/test/pi", WorkerPiSSHListenAddr: ":3334",
		WorkerSSHListenAddr: ":2222", WorkerClaudeSSHListenAddr: ":3333"}
	require.NoError(t, cfg.validatePiRuntime())
	cfg.WorkerClaudeEnabled = true
	require.NoError(t, cfg.validatePiRuntime())
	cfg.WorkerPiSSHListenAddr = ":3333"
	require.Error(t, cfg.validatePiRuntime())
	cfg.WorkerClaudeEnabled = false
	require.NoError(t, cfg.validatePiRuntime())
	cfg.WorkerPiSSHListenAddr = ":2222"
	require.Error(t, cfg.validatePiRuntime())
	cfg.WorkerPiEnabled = false
	require.NoError(t, cfg.validatePiRuntime())
}
