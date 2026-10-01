package workerconfig

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
)

func TestPiConfigCannotReadOrOverwriteCodex(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "AGENTS.md")
	require.NoError(t, os.WriteFile(path, []byte("Codex instructions"), 0o600))
	service := NewService(home, "codex")
	before, err := service.Read()
	require.NoError(t, err)
	options := ChannelOptions{Service: service}
	params := mustJSON(RuntimeRequest{Engine: runtimeidentity.Pi, Input: mustJSON(map[string]string{
		"revision": before.Revision, "content": "must not overwrite", "baseUrl": "http://localhost",
	})})
	for _, method := range []string{"config.read", "config.agents.write", "config.provider.write"} {
		t.Run(method, func(t *testing.T) {
			result, err := handleRuntimeRequest(options, method, params)
			require.ErrorContains(t, err, "请通过 Pi CLI 管理原生配置")
			require.Nil(t, result)
		})
	}
	after, err := service.Read()
	require.NoError(t, err)
	require.Equal(t, before, after)
	entries, err := os.ReadDir(home)
	require.NoError(t, err)
	require.Len(t, entries, 1, "不能为 Pi 创建 Codex 配置或备份")
}

func TestPiRestartIsolatedAndPropagatesFailure(t *testing.T) {
	options := ChannelOptions{Service: NewService(t.TempDir(), "codex"), Claude: NewClaudeService(t.TempDir())}
	options.Service.SetRestart(func() error { t.Fatal("不能重启 Codex"); return nil })
	options.Claude.SetRestart(func() error { t.Fatal("不能重启 Claude"); return nil })
	params := mustJSON(RuntimeRequest{Engine: runtimeidentity.Pi})
	_, err := handleRuntimeRequest(options, "runtime.restart", params)
	require.ErrorContains(t, err, "尚未启用 Pi")
	calls := 0
	options.PiRestart = func() error { calls++; return nil }
	_, err = handleRuntimeRequest(options, "runtime.restart", params)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	failure := errors.New("Pi restart failed")
	options.PiRestart = func() error { return failure }
	_, err = handleRuntimeRequest(options, "runtime.restart", params)
	require.ErrorIs(t, err, failure)
}
