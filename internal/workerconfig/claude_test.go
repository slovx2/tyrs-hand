package workerconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestClaudeConfigNativeStorageRedactionAndBackups(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "settings.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"hooks":{"Stop":[]},"env":{"CUSTOM":"keep","ANTHROPIC_MODEL":"old"}}`), 0o600))
	service := NewClaudeService(home)
	current, err := service.Read()
	require.NoError(t, err)
	for index := 0; index < 6; index++ {
		current, err = service.UpdateProvider(ClaudeProviderInput{Revision: current.Revision,
			BaseURL: "http://127.0.0.1:4321", APIKey: fmt.Sprintf("secret-%d", index), AuthMethod: "auth-token", Model: "claude-test"})
		require.NoError(t, err)
	}
	encoded, err := json.Marshal(current)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret-")
	require.True(t, current.APIKeyConfigured)
	require.Equal(t, "ANTHROPIC_AUTH_TOKEN", current.EnvKey)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var stored map[string]any
	require.NoError(t, json.Unmarshal(data, &stored))
	require.Contains(t, stored, "hooks")
	env := stored["env"].(map[string]any)
	require.Equal(t, "keep", env["CUSTOM"])
	require.Equal(t, "secret-5", env["ANTHROPIC_AUTH_TOKEN"])
	require.NotContains(t, env, "ANTHROPIC_MODEL")
	require.NotContains(t, env, "ANTHROPIC_API_KEY")
	for index := 1; index <= 4; index++ {
		backup := fmt.Sprintf("%s.bak.%d", path, index)
		data, err := os.ReadFile(backup)
		require.NoError(t, err)
		require.Contains(t, string(data), fmt.Sprintf("secret-%d", 5-index))
		info, err := os.Stat(backup)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	require.NoFileExists(t, path+".bak.5")
	current, err = service.UpdateAgents(current.Revision, "Claude only")
	require.NoError(t, err)
	require.Equal(t, "Claude only", current.Agents)
	require.NoFileExists(t, filepath.Join(home, "AGENTS.md"))
	current, err = service.UpdateProvider(ClaudeProviderInput{Revision: current.Revision,
		BaseURL: current.BaseURL, AuthMethod: "auth-token", ClearAPIKey: true})
	require.NoError(t, err)
	require.False(t, current.APIKeyConfigured)
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(data), "secret-")
}

func TestClaudeConfigConflictAndInvalidInputDoNotOverwrite(t *testing.T) {
	service := NewClaudeService(t.TempDir())
	initial, err := service.Read()
	require.NoError(t, err)
	_, err = service.UpdateProvider(ClaudeProviderInput{Revision: initial.Revision, BaseURL: "https://user:secret@example.com", AuthMethod: "api-key", APIKey: "key"})
	require.ErrorContains(t, err, "Base URL")
	_, err = service.UpdateAgents("", "not saved")
	require.ErrorContains(t, err, "冲突")
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for _, content := range []string{"one", "two"} {
		wait.Add(1)
		go func() { defer wait.Done(); _, err := service.UpdateAgents(initial.Revision, content); results <- err }()
	}
	wait.Wait()
	first, second := <-results, <-results
	require.NotEqual(t, first == nil, second == nil, "同版本的并发更新只能一个生效")
	current, err := service.Read()
	require.NoError(t, err)
	_, err = service.UpdateProvider(ClaudeProviderInput{Revision: current.Revision, BaseURL: "http://localhost", AuthMethod: "unknown", APIKey: "key"})
	require.Error(t, err)
	require.NoFileExists(t, filepath.Join(service.home, "settings.json"))
}

func TestRuntimeConfigDispatchRequiresEngineAndIsolatesFiles(t *testing.T) {
	codexHome, claudeHome := t.TempDir(), t.TempDir()
	options := ChannelOptions{Service: NewService(codexHome, "codex"), Claude: NewClaudeService(claudeHome)}
	for _, params := range []string{`{}`, `{"engine":"unknown"}`, `{"engine":""}`} {
		_, err := handleRuntimeRequest(options, "config.read", json.RawMessage(params))
		require.Error(t, err)
	}
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		request := RuntimeRequest{Engine: engine}
		params, err := json.Marshal(request)
		require.NoError(t, err)
		result, err := handleRuntimeRequest(options, "config.read", params)
		require.NoError(t, err)
		config := result.(workerprotocol.WorkerConfig)
		request.Input = mustJSON(map[string]string{"revision": config.Revision, "content": string(engine)})
		params, err = json.Marshal(request)
		require.NoError(t, err)
		_, err = handleRuntimeRequest(options, "config.agents.write", params)
		require.NoError(t, err)
	}
	data, err := os.ReadFile(filepath.Join(codexHome, "AGENTS.md"))
	require.NoError(t, err)
	require.Equal(t, "codex", string(data))
	data, err = os.ReadFile(filepath.Join(claudeHome, "CLAUDE.md"))
	require.NoError(t, err)
	require.Equal(t, "claude-code", string(data))
	_, err = handleRuntimeRequest(options, "runtime.restart", mustJSON(RuntimeRequest{Engine: runtimeidentity.Claude}))
	require.ErrorContains(t, err, "未启用")
}

func TestClaudeConfigPreservesInvalidFileAndRequiresNewCredentialForAuthSwitch(t *testing.T) {
	service := NewClaudeService(t.TempDir())
	path := filepath.Join(service.home, "settings.json")
	for _, invalid := range []string{`null`, `[]`, `{"env":null}`, `{"env":{"KEY":42}}`, `{"model":42}`} {
		require.NoError(t, os.WriteFile(path, []byte(invalid), 0o600))
		_, err := service.UpdateProvider(ClaudeProviderInput{Revision: "old", BaseURL: "http://localhost", APIKey: "key", AuthMethod: "api-key"})
		require.Error(t, err)
		stored, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, invalid, string(stored), "无法解析原生配置时不得覆盖文件")
	}
	require.NoError(t, os.WriteFile(path, []byte(`{"env":{"ANTHROPIC_API_KEY":"old-key"}}`), 0o600))
	current, err := service.Read()
	require.NoError(t, err)
	_, err = service.UpdateProvider(ClaudeProviderInput{Revision: current.Revision, BaseURL: "http://localhost", AuthMethod: "auth-token"})
	require.ErrorContains(t, err, "必须填写")
	after, err := service.Read()
	require.NoError(t, err)
	require.Equal(t, current.Revision, after.Revision)
}
