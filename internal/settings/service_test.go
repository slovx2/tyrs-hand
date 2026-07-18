package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderSignatureAndURLValidation(t *testing.T) {
	record := providerRecord{AgentProvider: AgentProvider{ProviderType: "api-key", BaseURL: "https://api.example.com/v1"}, CredentialVersion: "v1"}
	first := signature(record)
	require.Len(t, first, 64)
	require.Equal(t, first, signature(record))
	record.CredentialVersion = "v2"
	require.NotEqual(t, first, signature(record))
	require.NoError(t, validateURL("", "URL"))
	require.NoError(t, validateURL("https://example.com/path", "URL"))
	require.Error(t, validateURL("relative/path", "URL"))
}

func TestWriteSecretFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	require.NoError(t, writeSecretFile(path, []byte("secret")))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "secret", string(data))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
