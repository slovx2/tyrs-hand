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
	require.NoError(t, os.WriteFile(valid, []byte("#!/bin/sh\nprintf 'codex-cli 0.142.5\\n'\n"), 0o700))
	require.NoError(t, ValidateVersion(context.Background(), valid))
	invalid := filepath.Join(dir, "invalid-codex")
	require.NoError(t, os.WriteFile(invalid, []byte("#!/bin/sh\nprintf 'codex-cli 1.0.0\\n'\n"), 0o700))
	require.Error(t, ValidateVersion(context.Background(), invalid))
	require.Error(t, ValidateVersion(context.Background(), filepath.Join(dir, "missing")))
}
