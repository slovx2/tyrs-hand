//go:build integration

package hostworker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeCodexMigrationRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "codex-migration")
}

type nativeMigrationSuccess struct{ Cwd, ItemType, Source, Target string }
type nativeMigrationTypeResult struct {
	ItemType  string
	Successes []nativeMigrationSuccess
	Failures  []json.RawMessage
}
type nativeMigrationHistory struct {
	ImportID      string
	ProviderID    string
	CompletedAtMs int64
	Successes     []nativeMigrationSuccess
	Failures      []json.RawMessage
}

func nativeMigrationHistories(t *testing.T, ctx context.Context, client *codex.SocketClient) map[string]nativeMigrationHistory {
	t.Helper()
	result := nativeMetadataCall[struct{ Data []nativeMigrationHistory }](t, ctx, client, "externalAgentConfig/import/readHistories", nil)
	histories := map[string]nativeMigrationHistory{}
	for _, item := range result.Data {
		require.NotEmpty(t, item.ImportID)
		require.NotContains(t, histories, item.ImportID)
		require.Empty(t, item.Failures)
		require.Positive(t, item.CompletedAtMs)
		require.NotEmpty(t, item.Successes)
		histories[item.ImportID] = item
	}
	return histories
}

// MIGRATION-003：迁移真实项目文件并检查完成通知，不预制历史或会话。
func verifyCodexNativeMigration(t *testing.T, ctx context.Context, client *codex.SocketClient, root string, registry *RuntimeRegistry, connection *ssh.Client) {
	t.Helper()
	otherGeneration := registry.entries[runtimeidentity.Claude].Runtime.Generation()
	sourceAgents := filepath.Join(root, "CLAUDE.md")
	targetAgents := filepath.Join(root, "AGENTS.md")
	sourceSkill := filepath.Join(root, ".claude", "skills", "native-import", "SKILL.md")
	targetSkill := filepath.Join(root, ".agents", "skills", "native-import", "SKILL.md")
	hookPath := filepath.Join(root, ".codex", "hooks.json")
	agentsBody := "REAL_MIGRATION_INSTRUCTIONS\n"
	skillBody := "---\nname: native-import\ndescription: Real migration fixture\n---\nIMPORTED_SKILL_CONTENT\n"
	writeNativeMetadataFile(t, sourceAgents, agentsBody)
	writeNativeMetadataFile(t, sourceSkill, skillBody)
	hookBody, err := json.Marshal(map[string]any{"hooks": map[string]any{"SessionStart": []map[string]any{{"hooks": []map[string]string{{"type": "command", "command": "printf NATIVE_HOOK_EFFECT"}}}}}})
	require.NoError(t, err)
	writeNativeMetadataFile(t, filepath.Join(root, ".claude", "settings.json"), string(hookBody))
	detected := nativeMetadataCall[struct {
		Items []json.RawMessage
	}](t, ctx, client, "externalAgentConfig/detect", map[string]any{"includeHome": false, "cwds": []string{root}, "migrationSource": "claude"})
	types := []string{}
	for _, raw := range detected.Items {
		var item struct{ Cwd, ItemType string }
		require.NoError(t, json.Unmarshal(raw, &item))
		require.Equal(t, root, item.Cwd)
		types = append(types, item.ItemType)
	}
	require.ElementsMatch(t, []string{"AGENTS_MD", "SKILLS", "HOOKS"}, types)
	require.NoFileExists(t, targetAgents)
	require.NoFileExists(t, targetSkill)
	require.NoFileExists(t, hookPath)
	events := client.Subscribe(codex.ThreadFilter{})
	defer events.Close()
	imported := nativeMetadataCall[struct{ ImportID string }](t, ctx, client, "externalAgentConfig/import", map[string]any{"migrationItems": detected.Items, "migrationSource": "claude", "providerId": "native-real-migration"})
	require.NotEmpty(t, imported.ImportID)
	completed := false
	for !completed {
		select {
		case <-ctx.Done():
			t.Fatal("真实迁移没有完成通知")
		case event, ok := <-events.Events():
			require.True(t, ok, "SSH 通知流意外关闭")
			if event.Method != "externalAgentConfig/import/completed" {
				continue
			}
			var notification struct {
				ImportID        string
				ItemTypeResults []nativeMigrationTypeResult
			}
			require.NoError(t, json.Unmarshal(event.Params, &notification))
			if notification.ImportID != imported.ImportID {
				continue
			}
			require.Len(t, notification.ItemTypeResults, 3)
			completedTypes := []string{}
			for _, result := range notification.ItemTypeResults {
				require.Empty(t, result.Failures)
				require.Len(t, result.Successes, 1)
				completedTypes = append(completedTypes, result.ItemType)
			}
			require.ElementsMatch(t, types, completedTypes)
			completed = true
		}
	}
	for path, expected := range map[string]string{sourceAgents: agentsBody, targetAgents: agentsBody, sourceSkill: skillBody, targetSkill: skillBody} {
		body, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		require.Equal(t, expected, string(body), "迁移不能只返回成功或破坏源文件")
	}
	importedHooks, err := os.ReadFile(hookPath)
	require.NoError(t, err)
	require.JSONEq(t, string(hookBody), string(importedHooks))
	histories := nativeMigrationHistories(t, ctx, client)
	require.Len(t, histories, 1)
	first := histories[imported.ImportID]
	require.Equal(t, "native-real-migration", first.ProviderID)
	require.Len(t, first.Successes, 3)
	actualTypes := []string{}
	for _, success := range first.Successes {
		require.Equal(t, root, success.Cwd)
		actualTypes = append(actualTypes, success.ItemType)
		if success.ItemType == "AGENTS_MD" {
			require.Equal(t, sourceAgents, success.Source)
			require.Equal(t, targetAgents, success.Target)
		}
	}
	require.ElementsMatch(t, types, actualTypes)
	// 仅记录刚验证过的真实导入，不能用 recordHistory 替代文件迁移。
	recorded := nativeMetadataCall[struct{ ImportID string }](t, ctx, client, "externalAgentConfig/import/recordHistory", map[string]any{
		"providerId":      "native-verified-migration-report",
		"itemTypeResults": []map[string]any{{"itemType": "AGENTS_MD", "failures": []any{}, "successes": []map[string]any{{"itemType": "AGENTS_MD", "cwd": root, "source": sourceAgents, "target": targetAgents}}}},
	})
	require.NotEmpty(t, recorded.ImportID)
	require.NotEqual(t, imported.ImportID, recorded.ImportID)
	histories = nativeMigrationHistories(t, ctx, client)
	require.Len(t, histories, 2)
	require.Equal(t, "native-verified-migration-report", histories[recorded.ImportID].ProviderID)
	require.Equal(t, []nativeMigrationSuccess{{Cwd: root, ItemType: "AGENTS_MD", Source: sourceAgents, Target: targetAgents}}, histories[recorded.ImportID].Successes)
	trustNativeMetadataProject(t, root)
	verifyNativeHookCatalog(t, ctx, client, root)
	require.NoError(t, registry.Restart(runtimeidentity.Codex))
	client = connectRuntimeSSH(t, ctx, connection, runtimeidentity.Codex)
	require.Equal(t, histories, nativeMigrationHistories(t, ctx, client), "导入历史必须跨进程保留")
	require.True(t, listNativeSkills(t, ctx, client, root)[targetSkill].Enabled)
	verifyNativeHookCatalog(t, ctx, client, root)
	require.Equal(t, otherGeneration, registry.entries[runtimeidentity.Claude].Runtime.Generation())
}
