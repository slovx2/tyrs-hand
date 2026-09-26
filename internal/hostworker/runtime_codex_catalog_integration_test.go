//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeCodexCatalogRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "codex-catalog")
}

func nativeMetadataCall[T any](t *testing.T, ctx context.Context, client *codex.SocketClient, method string, params any) T {
	t.Helper()
	var result T
	require.NoError(t, client.Call(ctx, method, params, &result), method)
	return result
}

func writeNativeMetadataFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func trustNativeMetadataProject(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, "codex", "config", "config.toml")
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	body = append(body, []byte(fmt.Sprintf("\n[projects.%q]\ntrust_level = \"trusted\"\n", root))...)
	require.NoError(t, os.WriteFile(path, body, 0o600))
}

type nativeSkill struct {
	Name, Path, Scope string
	Enabled           bool
}

func listNativeSkills(t *testing.T, ctx context.Context, client *codex.SocketClient, root string) map[string]nativeSkill {
	t.Helper()
	result := nativeMetadataCall[struct {
		Data []struct {
			Cwd    string
			Skills []nativeSkill
			Errors []json.RawMessage
		}
	}](t, ctx, client, "skills/list", map[string]any{"cwds": []string{root}, "forceReload": true})
	require.Len(t, result.Data, 1)
	require.Equal(t, root, result.Data[0].Cwd)
	require.Empty(t, result.Data[0].Errors)
	skills := map[string]nativeSkill{}
	for _, skill := range result.Data[0].Skills {
		skills[skill.Path] = skill
	}
	return skills
}

func nativeFeatureStates(t *testing.T, ctx context.Context, client *codex.SocketClient) map[string]bool {
	t.Helper()
	states := map[string]bool{}
	var cursor *string
	seen := map[string]bool{}
	for page := 0; page < 100; page++ {
		result := nativeMetadataCall[struct {
			Data []struct {
				Name    string
				Enabled bool
			}
			NextCursor *string
		}](t, ctx, client, "experimentalFeature/list", map[string]any{"cursor": cursor, "limit": 2})
		require.LessOrEqual(t, len(result.Data), 2)
		for _, feature := range result.Data {
			require.NotEmpty(t, feature.Name)
			require.NotContains(t, states, feature.Name, "分页不能重复特性")
			states[feature.Name] = feature.Enabled
		}
		cursor = result.NextCursor
		if cursor == nil {
			break
		}
		require.False(t, seen[*cursor], "游标不能循环")
		seen[*cursor] = true
	}
	require.Nil(t, cursor, "特性目录分页必须终止")
	require.Greater(t, len(states), 2)
	return states
}

func verifyNativeHookCatalog(t *testing.T, ctx context.Context, client *codex.SocketClient, root string) {
	t.Helper()
	result := nativeMetadataCall[struct {
		Data []struct {
			Cwd              string
			Errors, Warnings []json.RawMessage
			Hooks            []struct {
				Command, Source, SourcePath, EventName, HandlerType, TrustStatus, CurrentHash string
				Enabled                                                                       bool
			}
		}
	}](t, ctx, client, "hooks/list", map[string]any{"cwds": []string{root}})
	require.Len(t, result.Data, 1)
	entry := result.Data[0]
	require.Equal(t, root, entry.Cwd)
	require.Empty(t, entry.Errors)
	require.Empty(t, entry.Warnings)
	require.Len(t, entry.Hooks, 1)
	hook := entry.Hooks[0]
	require.Equal(t, "printf NATIVE_HOOK_EFFECT", hook.Command)
	require.Equal(t, "project", hook.Source)
	require.Equal(t, filepath.Join(root, ".codex", "hooks.json"), hook.SourcePath)
	require.Equal(t, "sessionStart", hook.EventName)
	require.Equal(t, "command", hook.HandlerType)
	require.Equal(t, "untrusted", hook.TrustStatus, "项目可信不能自动授权 Hook 执行")
	require.Contains(t, hook.CurrentHash, "sha256:")
	require.True(t, hook.Enabled)
}

// CATALOG-002：真实目录和配置状态，零模型调用；Hook 仅验收目录，不声称执行。
func verifyCodexNativeCatalog(t *testing.T, ctx context.Context, client *codex.SocketClient, root string, registry *RuntimeRegistry, connection *ssh.Client) {
	t.Helper()
	otherGeneration := registry.entries[runtimeidentity.Claude].Runtime.Generation()
	nativePath := filepath.Join(root, ".agents", "skills", "native-catalog", "SKILL.md")
	extraRoot := filepath.Join(root, "extra-skills")
	extraPath := filepath.Join(extraRoot, "extra-catalog", "SKILL.md")
	writeNativeMetadataFile(t, nativePath, "---\nname: native-catalog\ndescription: Native directory fixture\n---\nNATIVE_SKILL_CONTENT\n")
	writeNativeMetadataFile(t, extraPath, "---\nname: extra-catalog\ndescription: Extra directory fixture\n---\nEXTRA_SKILL_CONTENT\n")
	skills := listNativeSkills(t, ctx, client, root)
	require.Equal(t, nativeSkill{Name: "native-catalog", Path: nativePath, Scope: "repo", Enabled: true}, skills[nativePath])
	require.NotContains(t, skills, extraPath)
	nativeMetadataCall[struct{}](t, ctx, client, "skills/extraRoots/set", map[string]any{"extraRoots": []string{extraRoot}})
	require.True(t, listNativeSkills(t, ctx, client, root)[extraPath].Enabled)
	written := nativeMetadataCall[struct{ EffectiveEnabled bool }](t, ctx, client, "skills/config/write", map[string]any{"path": nativePath, "enabled": false})
	require.False(t, written.EffectiveEnabled)
	skills = listNativeSkills(t, ctx, client, root)
	require.Contains(t, skills, nativePath)
	require.False(t, skills[nativePath].Enabled)
	require.True(t, skills[extraPath].Enabled)
	config, err := os.ReadFile(filepath.Join(root, "codex", "config", "config.toml"))
	require.NoError(t, err)
	require.Contains(t, string(config), nativePath)
	require.Contains(t, string(config), "enabled = false")
	before := nativeFeatureStates(t, ctx, client)
	require.Contains(t, before, "memories")
	for _, enabled := range []bool{!before["memories"], before["memories"]} {
		changed := nativeMetadataCall[struct{ Enablement map[string]bool }](t, ctx, client, "experimentalFeature/enablement/set", map[string]any{"enablement": map[string]bool{"memories": enabled}})
		require.Equal(t, map[string]bool{"memories": enabled}, changed.Enablement)
		expected := make(map[string]bool, len(before))
		for name, value := range before {
			expected[name] = value
		}
		expected["memories"] = enabled
		require.Equal(t, expected, nativeFeatureStates(t, ctx, client), "只修改目标开关")
	}
	hookBody, err := json.Marshal(map[string]any{"hooks": map[string]any{"SessionStart": []map[string]any{{"hooks": []map[string]string{{"type": "command", "command": "printf NATIVE_HOOK_EFFECT"}}}}}})
	require.NoError(t, err)
	writeNativeMetadataFile(t, filepath.Join(root, ".codex", "hooks.json"), string(hookBody))
	trustNativeMetadataProject(t, root)
	verifyNativeHookCatalog(t, ctx, client, root)
	require.NoError(t, registry.Restart(runtimeidentity.Codex))
	client = connectRuntimeSSH(t, ctx, connection, runtimeidentity.Codex)
	skills = listNativeSkills(t, ctx, client, root)
	require.Contains(t, skills, nativePath)
	require.False(t, skills[nativePath].Enabled, "原生禁用配置必须跨进程保留")
	require.NotContains(t, skills, extraPath, "extraRoots 是当前进程配置")
	nativeMetadataCall[struct{}](t, ctx, client, "skills/extraRoots/set", map[string]any{"extraRoots": []string{extraRoot}})
	require.True(t, listNativeSkills(t, ctx, client, root)[extraPath].Enabled)
	verifyNativeHookCatalog(t, ctx, client, root)
	require.Equal(t, otherGeneration, registry.entries[runtimeidentity.Claude].Runtime.Generation())
}
