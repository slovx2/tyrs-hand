package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateVersion(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid-codex")
	require.NoError(t, os.WriteFile(valid, []byte("#!/bin/sh\nprintf 'codex-cli 0.147.0\\n'\n"), 0o700))
	require.NoError(t, ValidateVersion(context.Background(), valid))
	newer := filepath.Join(dir, "newer-codex")
	require.NoError(t, os.WriteFile(newer, []byte("#!/bin/sh\nprintf 'codex-cli 1.0.0\\n'\n"), 0o700))
	require.Error(t, ValidateVersion(context.Background(), newer))
	old := filepath.Join(dir, "old-codex")
	require.NoError(t, os.WriteFile(old, []byte("#!/bin/sh\nprintf 'codex-cli 0.146.9\\n'\n"), 0o700))
	require.Error(t, ValidateVersion(context.Background(), old))
	prerelease := filepath.Join(dir, "prerelease-codex")
	require.NoError(t, os.WriteFile(prerelease,
		[]byte("#!/bin/sh\nprintf 'codex-cli 0.147.0-beta.1\\n'\n"), 0o700))
	require.Error(t, ValidateVersion(context.Background(), prerelease))
	invalid := filepath.Join(dir, "invalid-codex")
	require.NoError(t, os.WriteFile(invalid, []byte("#!/bin/sh\nprintf 'unknown\\n'\n"), 0o700))
	require.Error(t, ValidateVersion(context.Background(), invalid))
	require.Error(t, ValidateVersion(context.Background(), filepath.Join(dir, "missing")))
}
