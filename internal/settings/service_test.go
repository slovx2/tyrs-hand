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

func TestProviderConnectionChangedIgnoresRuntimeDefaults(t *testing.T) {
	old := providerRecord{AgentProvider: AgentProvider{
		ProviderType: "api-key", BaseURL: "https://api.example.com/v1", ProxyURL: "https://proxy.example.com",
		Model: "gpt-5.5", Reasoning: "medium", ServiceTier: "standard",
	}, CredentialVersion: "v1"}
	current := old
	current.Model = "gpt-5.6-sol"
	current.Reasoning = "xhigh"
	current.ServiceTier = "fast"
	require.False(t, providerConnectionChanged(old, current))

	current.CredentialVersion = "v2"
	require.True(t, providerConnectionChanged(old, current))
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

func TestWriteProviderConfigPreservesCodexSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("openai_base_url = \"https://old.example/v1\"\n[projects.\"/repo\"]\ntrust_level = \"trusted\"\n"), 0o600))
	require.NoError(t, writeProviderConfig(path, "https://api.example.com/v1"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "openai_base_url = \"https://api.example.com/v1\"\n[projects.\"/repo\"]\ntrust_level = \"trusted\"\n", string(data))
}
