package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveCodexRealPrefersUserOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TYRS_HAND_CODEX_REAL", "")
	require.Equal(t, defaultCodexReal, resolveCodexReal())

	userBinary := filepath.Join(home, ".local", "share", "tyrs-hand", "codex",
		"current", "bin", "codex")
	require.NoError(t, os.MkdirAll(filepath.Dir(userBinary), 0o700))
	require.NoError(t, os.WriteFile(userBinary, []byte("#!/bin/sh\n"), 0o700))
	require.Equal(t, userBinary, resolveCodexReal())

	t.Setenv("TYRS_HAND_CODEX_REAL", "/tmp/codex-explicit")
	require.Equal(t, "/tmp/codex-explicit", resolveCodexReal())
}
