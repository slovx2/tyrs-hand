package workerconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/require"
)

func browserRegistrationFixture(t *testing.T) BrowserRegistration {
	t.Helper()
	home := t.TempDir()
	return BrowserRegistration{Home: home, CodexHome: filepath.Join(home, ".codex"),
		StateDir: filepath.Join(home, "state"), URL: "http://127.0.0.1:8931/mcp", Token: "test-derived-token"}
}

func browserRegisteredFiles(options BrowserRegistration) []string {
	pi := options.PiAgentDir
	if pi == "" {
		pi = filepath.Join(options.Home, ".pi", "agent")
	}
	return []string{filepath.Join(options.CodexHome, "config.toml"),
		filepath.Join(options.Home, ".claude.json"), filepath.Join(pi, "mcp.json")}
}

func TestRegisterBrowserMCPPreservesConfigAndIsIdempotent(t *testing.T) {
	options := browserRegistrationFixture(t)
	options.PiAgentDir = filepath.Join(options.Home, "custom-pi")
	files := browserRegisteredFiles(options)
	initial := []string{
		"model = 'personal-model'\n[mcp_servers.other]\nurl = 'https://example.com/mcp'\n",
		`{"preferences":{"theme":"dark"},"mcpServers":{"other":{"command":"example"}}}`,
		`{"mcpServers":{"other":{"command":"example"}}}`,
	}
	for i, path := range files {
		require.NoError(t, atomicWrite(path, []byte(initial[i]), 0o644))
	}
	require.NoError(t, RegisterBrowserMCP(options))
	for i, path := range files {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		parsed := map[string]any{}
		key, header := "mcpServers", "headers"
		if i == 0 {
			require.NoError(t, toml.Unmarshal(data, &parsed))
			key, header = "mcp_servers", "http_headers"
			require.Equal(t, "personal-model", parsed["model"])
		} else {
			require.NoError(t, json.Unmarshal(data, &parsed))
		}
		servers := parsed[key].(map[string]any)
		require.Contains(t, servers, "other")
		chrome := servers["chrome"].(map[string]any)
		require.Equal(t, options.URL, chrome["url"])
		require.Equal(t, "Bearer "+options.Token, chrome[header].(map[string]any)["Authorization"])
		require.NotContains(t, chrome[header], "X-Tyrs-Browser-Task-Id")
		if i == 1 {
			require.Equal(t, float64(120000), chrome["timeout"])
			require.Contains(t, parsed, "preferences")
		}
		if i == 2 {
			require.Equal(t, "direct", chrome["exposure"])
			require.Equal(t, float64(120), chrome["timeout"])
		}
		stat, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), stat.Mode().Perm())
		require.NoError(t, RegisterBrowserMCP(options))
		again, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, stat.ModTime(), again.ModTime())
		require.NoFileExists(t, path+".bak.2")
		backup, err := os.ReadFile(path + ".bak.1")
		require.NoError(t, err)
		require.Equal(t, initial[i], string(backup))
	}
}

func TestRegisterBrowserMCPRotatesCredentialsAndFourBackups(t *testing.T) {
	options := browserRegistrationFixture(t)
	require.NoError(t, RegisterBrowserMCP(options))
	for i := range 6 {
		options.Token = fmt.Sprintf("new-token-%d", i)
		options.URL = fmt.Sprintf("http://127.0.0.1:%d/mcp", 9000+i)
		require.NoError(t, RegisterBrowserMCP(options))
	}
	for _, path := range browserRegisteredFiles(options) {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Contains(t, string(data), "new-token-5")
		for i := 1; i <= 4; i++ {
			info, err := os.Stat(fmt.Sprintf("%s.bak.%d", path, i))
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		}
		require.NoFileExists(t, path+".bak.5")
	}
}

func TestRegisterBrowserMCPConflictAndMalformedFilesAreIndependent(t *testing.T) {
	options := browserRegistrationFixture(t)
	files := browserRegisteredFiles(options)
	conflict := []byte("[mcp_servers.chrome]\nurl = 'https://other.invalid/mcp'\n")
	invalid := []byte(`{"private":"do-not-print-this-secret",broken`)
	require.NoError(t, atomicWrite(files[0], conflict, 0o600))
	require.NoError(t, atomicWrite(files[1], invalid, 0o600))
	err := RegisterBrowserMCP(options)
	require.ErrorContains(t, err, "chrome 已由其他服务占用")
	require.ErrorContains(t, err, "用户配置格式无效")
	require.NotContains(t, err.Error(), "do-not-print-this-secret")
	require.NotContains(t, err.Error(), options.Token)
	for i, expected := range [][]byte{conflict, invalid} {
		actual, readErr := os.ReadFile(files[i])
		require.NoError(t, readErr)
		require.Equal(t, expected, actual)
		require.NoFileExists(t, files[i]+".bak.1")
	}
	require.FileExists(t, files[2])
}
