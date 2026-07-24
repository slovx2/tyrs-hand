package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExactCodexVersion(t *testing.T) {
	for _, version := range []string{"0.145.0", "1.2.3-beta.1"} {
		require.True(t, exactVersion.MatchString(version))
	}
	for _, version := range []string{"latest", "0.145", "^0.145.0", "0.145.x", ""} {
		require.False(t, exactVersion.MatchString(version))
	}
}

func TestCodexVersionSwitchRollbackAndReset(t *testing.T) {
	root := filepath.Join(t.TempDir(), "codex")
	marker := filepath.Join(t.TempDir(), "state", "codex-restart-required")
	first := filepath.Join(root, "versions", "0.145.0")
	second := filepath.Join(root, "versions", "0.146.0")
	require.NoError(t, os.MkdirAll(first, 0o700))
	require.NoError(t, os.MkdirAll(second, 0o700))

	require.NoError(t, switchVersion(root, first))
	require.NoError(t, switchVersion(root, second))
	current, err := os.Readlink(filepath.Join(root, "current"))
	require.NoError(t, err)
	require.Equal(t, second, current)
	previous, err := os.Readlink(filepath.Join(root, "previous"))
	require.NoError(t, err)
	require.Equal(t, first, previous)

	require.NoError(t, rollbackCodex(root, marker))
	current, err = os.Readlink(filepath.Join(root, "current"))
	require.NoError(t, err)
	require.Equal(t, first, current)
	require.FileExists(t, marker)

	require.NoError(t, resetCodex(root, marker))
	require.NoFileExists(t, filepath.Join(root, "current"))
	previous, err = os.Readlink(filepath.Join(root, "previous"))
	require.NoError(t, err)
	require.Equal(t, first, previous)
}
