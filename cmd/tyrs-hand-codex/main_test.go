package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunReplacesProxyProcessWithRealCodex(t *testing.T) {
	original := execProcess
	t.Cleanup(func() { execProcess = original })
	t.Setenv("TYRS_HAND_CODEX_REAL", "/opt/test/codex")
	t.Setenv("CODEX_HOME", "")
	wantErr := errors.New("exec intercepted")
	var binary string
	var arguments, environment []string
	execProcess = func(gotBinary string, gotArguments, gotEnvironment []string) error {
		binary = gotBinary
		arguments = append([]string(nil), gotArguments...)
		environment = append([]string(nil), gotEnvironment...)
		return wantErr
	}

	err := run([]string{"app-server", "--listen", "unix:///run/test.sock"})
	require.ErrorIs(t, err, wantErr)
	require.Equal(t, "/opt/test/codex", binary)
	require.Equal(t, []string{os.Args[0], "app-server", "--listen",
		"unix:///run/test.sock"}, arguments)
	require.True(t, slices.Contains(environment,
		"CODEX_HOME=/var/lib/tyrs-hand/codex"))
}

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
