//go:build integration

package hostworker

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeCodexPluginsRealSSH(t *testing.T) { testRuntimeRegistryRealSSH(t, "codex-plugins") }

const runtimePluginName = "ssh-fixture"
const runtimePluginID = "ssh-fixture@ssh-local"

type runtimeCodexPluginsFixture struct {
	root, marker string
	calls        atomic.Int64
}

func newRuntimeCodexPluginsFixture(root string) *runtimeCodexPluginsFixture {
	return &runtimeCodexPluginsFixture{root: root, marker: "PLUGIN_SKILL_BODY_" + rand.Text()}
}

func (f *runtimeCodexPluginsFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, body []byte) {
	t.Helper()
	require.Equal(t, "/v1/responses", request.URL.Path)
	step := f.calls.Add(1)
	require.LessOrEqual(t, step, int64(5), "插件管理不能额外调用模型")
	var parsed struct {
		Tools []struct{ Name string }
		Input []struct {
			Type   string
			CallID string `json:"call_id"`
			Output string
		}
	}
	require.NoError(t, json.Unmarshal(body, &parsed))
	if step == 5 {
		require.True(t, strings.Contains(string(body), "PLUGIN_AFTER_UNINSTALL"))
		require.False(t, strings.Contains(string(body), f.marker), "卸载后新原生会话不能加载插件技能内容")
		runtimeTextModel(w, request, "PLUGIN_REMOVED", "plugin-removed")
		return
	}
	require.True(t, strings.Contains(string(body), f.marker), "技能正文必须由真实CLI加载，不能只传普通用户指令")
	round := (step + 1) / 2
	id := fmt.Sprintf("plugin-skill-%d", round)
	if step%2 == 0 {
		found := false
		for _, item := range parsed.Input {
			if item.Type == "function_call_output" && item.CallID == id {
				require.Contains(t, item.Output, "PLUGIN_EFFECT_WRITTEN")
				found = true
			}
		}
		require.True(t, found, "真实插件技能工具结果必须回模")
		content, err := os.ReadFile(filepath.Join(f.root, "project", "plugin-effect.txt"))
		require.NoError(t, err)
		require.Equal(t, strings.Repeat("PLUGIN_EFFECT\n", int(round)), string(content))
		runtimeTextModel(w, request, "PLUGIN_SKILL_DONE", id+"-done")
		return
	}
	declared := false
	for _, tool := range parsed.Tools {
		declared = declared || tool.Name == "exec_command"
	}
	require.True(t, declared, "只能调用真实CLI声明的shell工具")
	args, err := json.Marshal(map[string]any{"cmd": "printf 'PLUGIN_EFFECT\\n' >> plugin-effect.txt; printf PLUGIN_EFFECT_WRITTEN", "workdir": filepath.Join(f.root, "project"), "login": false})
	require.NoError(t, err)
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, value map[string]any) {
		value["type"] = kind
		data, err := json.Marshal(value)
		require.NoError(t, err)
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
		require.NoError(t, err)
	}
	event("response.created", map[string]any{"response": map[string]any{"id": id}})
	event("response.output_item.done", map[string]any{"item": map[string]any{"type": "function_call", "name": "exec_command", "call_id": id, "arguments": string(args)}})
	event("response.completed", map[string]any{"response": map[string]any{"id": id, "usage": map[string]int{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}})
}

type runtimePluginSummary struct {
	ID, Name           string
	Enabled, Installed bool
}

type runtimePluginListing struct {
	Marketplaces []struct {
		Name, Path string
		Plugins    []runtimePluginSummary
	}
	MarketplaceLoadErrors []json.RawMessage
}

func runtimePluginFind(list runtimePluginListing) (runtimePluginSummary, bool) {
	for _, marketplace := range list.Marketplaces {
		for _, plugin := range marketplace.Plugins {
			if plugin.ID == runtimePluginID {
				return plugin, true
			}
		}
	}
	return runtimePluginSummary{}, false
}

func runtimePluginList(t *testing.T, ctx context.Context, client *codex.SocketClient, method, project string) runtimePluginListing {
	t.Helper()
	params := map[string]any{"cwds": []string{project}}
	if method == "plugin/list" {
		params["marketplaceKinds"] = []string{"local"}
		params["forceRefetch"] = false
	}
	var listed runtimePluginListing
	require.NoError(t, client.Call(ctx, method, params, &listed))
	require.Empty(t, listed.MarketplaceLoadErrors)
	return listed
}

func runtimePluginTurn(t *testing.T, ctx context.Context, client *codex.SocketClient, threadID, skillName, skillPath, text string) {
	t.Helper()
	input := []map[string]any{{"type": "text", "text": text}}
	if skillPath != "" {
		input = append(input, map[string]any{"type": "skill", "name": skillName, "path": skillPath})
	}
	events := client.Subscribe(codex.ThreadFilter{ThreadID: threadID})
	defer events.Close()
	var started struct{ Turn struct{ ID string } }
	require.NoError(t, client.Call(ctx, "turn/start", map[string]any{"threadId": threadID, "input": input}, &started))
	runtimeReviewWait(t, ctx, events, threadID, started.Turn.ID)
	var history runtimeReviewThread
	var response struct{ Thread runtimeReviewThread }
	require.NoError(t, client.Call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true}, &response))
	history = response.Thread
	found := false
	for _, turn := range history.Turns {
		if turn.ID != started.Turn.ID {
			continue
		}
		require.Equal(t, "completed", turn.Status)
		for _, item := range turn.Items {
			var value struct{ Type, Text string }
			require.NoError(t, json.Unmarshal(item, &value))
			if value.Type == "agentMessage" {
				found = found || value.Text == "PLUGIN_SKILL_DONE" || value.Text == "PLUGIN_REMOVED"
			}
		}
	}
	require.True(t, found, "原生插件执行结果必须保存在真实历史")
}

func verifyRuntimeCodexPlugins(t *testing.T, ctx context.Context, registry *RuntimeRegistry, connection *ssh.Client, fixture *runtimeCodexPluginsFixture, root string) {
	t.Helper()
	project := filepath.Join(root, "project")
	marketplace := filepath.Join(project, ".agents", "plugins", "marketplace.json")
	source := filepath.Join(project, runtimePluginName)
	skillBody := "---\nname: write-effect\ndescription: Write the local fixture effect\n---\n" + fixture.marker + "\nAppend PLUGIN_EFFECT to plugin-effect.txt using the shell tool, then report completion.\n"
	writeNativeMetadataFile(t, filepath.Join(source, ".codex-plugin", "plugin.json"), `{"name":"ssh-fixture","version":"1.0.0","description":"Temporary local SSH acceptance plugin"}`)
	writeNativeMetadataFile(t, filepath.Join(source, "skills", "write-effect", "SKILL.md"), skillBody)
	writeNativeMetadataFile(t, marketplace, `{"name":"ssh-local","plugins":[{"name":"ssh-fixture","source":{"source":"local","path":"./ssh-fixture"}}]}`)
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".git"), 0o700))
	otherGeneration := registry.entries[runtimeidentity.Claude].Runtime.Generation()
	client, trace := connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Codex, codex.SocketClientOptions{})
	require.NoError(t, client.Call(ctx, "config/value/write", map[string]any{"keyPath": "features.plugins", "value": true, "mergeStrategy": "upsert"}, nil))
	listed := runtimePluginList(t, ctx, client, "plugin/list", project)
	initial, found := runtimePluginFind(listed)
	require.True(t, found, "本地插件必须被真实目录发现")
	require.False(t, initial.Installed)
	_, found = runtimePluginFind(runtimePluginList(t, ctx, client, "plugin/installed", project))
	require.False(t, found)
	var detail struct {
		Plugin struct {
			Summary                          runtimePluginSummary
			MarketplaceName, MarketplacePath string
			Skills                           []struct {
				Name, Path string
				Enabled    bool
			}
		}
	}
	readParams := map[string]any{"marketplacePath": marketplace, "pluginName": runtimePluginName}
	require.NoError(t, client.Call(ctx, "plugin/read", readParams, &detail))
	require.Equal(t, runtimePluginID, detail.Plugin.Summary.ID)
	require.Equal(t, "ssh-local", detail.Plugin.MarketplaceName)
	require.Len(t, detail.Plugin.Skills, 1)
	require.Equal(t, int64(0), fixture.calls.Load())
	var installed struct {
		AuthPolicy      string
		AppsNeedingAuth []json.RawMessage
	}
	require.NoError(t, client.Call(ctx, "plugin/install", readParams, &installed))
	require.Empty(t, installed.AppsNeedingAuth)
	installedSummary, found := runtimePluginFind(runtimePluginList(t, ctx, client, "plugin/installed", project))
	require.True(t, found)
	require.True(t, installedSummary.Installed)
	require.True(t, installedSummary.Enabled)
	configPath := filepath.Join(root, "codex", "config", "config.toml")
	config, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(config), runtimePluginID)
	skills := listNativeSkills(t, ctx, client, project)
	var skill nativeSkill
	for _, candidate := range skills {
		if strings.Contains(candidate.Name, "write-effect") {
			skill = candidate
		}
	}
	require.NotEmpty(t, skill.Path)
	require.True(t, skill.Enabled)
	require.True(t, strings.HasPrefix(skill.Path, filepath.Join(root, "codex", "config", "plugins", "cache")+string(filepath.Separator)), "技能必须来自真实安装缓存")
	actual, err := os.ReadFile(skill.Path)
	require.NoError(t, err)
	require.Equal(t, skillBody, string(actual))
	pluginRoot := filepath.Dir(filepath.Dir(filepath.Dir(skill.Path)))
	manifest, err := os.ReadFile(filepath.Join(pluginRoot, ".codex-plugin", "plugin.json"))
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"ssh-fixture","version":"1.0.0","description":"Temporary local SSH acceptance plugin"}`, string(manifest))
	// 无效安装不能破坏已有插件配置或可执行技能。
	require.Error(t, client.Call(ctx, "plugin/install", map[string]any{"marketplacePath": marketplace, "pluginName": "missing-fixture"}, nil))
	afterFailure, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Equal(t, config, afterFailure)
	require.Equal(t, int64(0), fixture.calls.Load(), "安装和读取插件不能调用模型")
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{"cwd": project, "approvalPolicy": "never", "sandbox": "danger-full-access"})
	runtimePluginTurn(t, ctx, client, thread.ID, skill.Name, skill.Path, "PLUGIN_INSTALLED: execute the selected fixture skill once")
	require.Equal(t, int64(2), fixture.calls.Load())
	trace.expectClose("runtime-restart")
	require.NoError(t, registry.Restart(runtimeidentity.Codex))
	require.Equal(t, otherGeneration, registry.entries[runtimeidentity.Claude].Runtime.Generation())
	client, trace = connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Codex, codex.SocketClientOptions{})
	afterRestart, found := runtimePluginFind(runtimePluginList(t, ctx, client, "plugin/installed", project))
	require.True(t, found)
	require.Equal(t, installedSummary, afterRestart)
	require.Equal(t, skill, listNativeSkills(t, ctx, client, project)[skill.Path])
	// 显式恢复本专项的执行策略，避免原生resume采用默认workspaceWrite后嵌套OS沙箱。
	readSessionThread(t, ctx, client, "thread/resume", map[string]any{"threadId": thread.ID, "approvalPolicy": "never", "sandbox": "danger-full-access"})
	require.Equal(t, int64(2), fixture.calls.Load())
	runtimePluginTurn(t, ctx, client, thread.ID, skill.Name, skill.Path, "PLUGIN_RESTARTED: execute the selected fixture skill once")
	require.Equal(t, int64(4), fixture.calls.Load())
	require.NoError(t, client.Call(ctx, "plugin/uninstall", map[string]any{"pluginId": runtimePluginID}, nil))
	_, found = runtimePluginFind(runtimePluginList(t, ctx, client, "plugin/installed", project))
	require.False(t, found)
	removed, found := runtimePluginFind(runtimePluginList(t, ctx, client, "plugin/list", project))
	require.True(t, found, "卸载不能删除本地marketplace的源目录")
	require.False(t, removed.Installed)
	require.False(t, removed.Enabled)
	require.NotContains(t, listNativeSkills(t, ctx, client, project), skill.Path)
	afterUninstall, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NotEqual(t, config, afterUninstall, "卸载必须真实更新持久化配置")
	trace.expectClose("runtime-restart")
	require.NoError(t, registry.Restart(runtimeidentity.Codex))
	client, _ = connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Codex, codex.SocketClientOptions{})
	_, found = runtimePluginFind(runtimePluginList(t, ctx, client, "plugin/installed", project))
	require.False(t, found, "卸载必须跨运行时重启保留")
	require.NotContains(t, listNativeSkills(t, ctx, client, project), skill.Path)
	fresh := readSessionThread(t, ctx, client, "thread/start", map[string]any{"cwd": project, "approvalPolicy": "never", "sandbox": "danger-full-access"})
	runtimePluginTurn(t, ctx, client, fresh.ID, "", "", "PLUGIN_AFTER_UNINSTALL: reply without running any tools")
	effect, err := os.ReadFile(filepath.Join(project, "plugin-effect.txt"))
	require.NoError(t, err)
	require.Equal(t, "PLUGIN_EFFECT\nPLUGIN_EFFECT\n", string(effect))
	sourceBody, err := os.ReadFile(filepath.Join(source, "skills", "write-effect", "SKILL.md"))
	require.NoError(t, err)
	require.Equal(t, skillBody, string(sourceBody), "卸载只能变更安装状态，不能删除共享项目源文件")
	_, err = os.Stat(filepath.Join(root, "claude-code", "config", "plugins"))
	require.True(t, os.IsNotExist(err), "Codex插件缓存不能写进Claude配置目录")
	require.Equal(t, otherGeneration, registry.entries[runtimeidentity.Claude].Runtime.Generation())
	require.Equal(t, int64(5), fixture.calls.Load())
}
