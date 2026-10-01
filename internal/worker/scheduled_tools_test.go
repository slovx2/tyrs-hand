package worker

import (
	"encoding/json"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestAutomationSpecIsInjectedForWorkspaceProjects(t *testing.T) {
	directory := workspaceGitTools(&workerprotocol.WorkspaceProjectContext{
		WorkspaceKind: "directory",
	})
	require.Len(t, directory, 1)
	require.True(t, hasDynamicTool(directory, "tyrs_hand", "automation_update"))
	require.False(t, hasDynamicTool(directory, "", "generate_image"))
	require.False(t, hasDynamicTool(directory, "git", "status"))

	git := workspaceGitTools(&workerprotocol.WorkspaceProjectContext{
		WorkspaceKind: "git", CloneURL: "https://example.invalid/repository.git",
	})
	require.True(t, hasDynamicTool(git, "tyrs_hand", "automation_update"))
	require.True(t, hasDynamicTool(git, "git", "status"))
	require.False(t, hasDynamicTool(git, "", "generate_image"))
}

func TestPiToolInjectionPreservesBusinessToolsOnly(t *testing.T) {
	cfg := config.Config{BrowserMCPURL: "http://127.0.0.1:8931/mcp"}
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude, runtimeidentity.Pi} {
		t.Run(string(engine), func(t *testing.T) {
			controller := &desktopController{processor: &Processor{cfg: cfg, runtimeIdentity: runtimeidentity.Identity{Engine: engine}}, workspace: &workspaceCodex{}}
			params := controller.injectDesktopRuntime(json.RawMessage(`{"cwd":"/tmp/project"}`), desktopRuntimeInjection{includeDynamicTools: true, includeBrowserMCP: true})
			var result struct {
				DynamicTools []ports.DynamicToolSpec `json:"dynamicTools"`
				Config       map[string]any          `json:"config"`
			}
			require.NoError(t, json.Unmarshal(params, &result))
			for _, specs := range [][]ports.DynamicToolSpec{result.DynamicTools, workspaceRuntimeTools(cfg, &workerprotocol.WorkspaceProjectContext{WorkspaceKind: "git"}, engine)} {
				require.True(t, hasDynamicTool(specs, "git", "status"))
				require.True(t, hasDynamicTool(specs, "tyrs_hand", "automation_update"))
				require.Equal(t, engine != runtimeidentity.Pi, hasDynamicTool(specs, "", "generate_image"))
				if engine == runtimeidentity.Pi {
					require.Len(t, specs, 2)
				}
			}
			if engine == runtimeidentity.Pi {
				require.NotContains(t, result.Config, "mcp_servers")
			}
		})
	}
}

func TestDesktopNewThreadReceivesAutomationSpec(t *testing.T) {
	controller := &desktopController{processor: &Processor{}, workspace: &workspaceCodex{}}
	params := controller.injectDesktopRuntime(json.RawMessage(`{"cwd":"/tmp/project"}`),
		desktopRuntimeInjection{includeDynamicTools: true})
	var value struct {
		DynamicTools []struct {
			Name  string `json:"name"`
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"dynamicTools"`
	}
	require.NoError(t, json.Unmarshal(params, &value))
	found := false
	for _, namespace := range value.DynamicTools {
		for _, tool := range namespace.Tools {
			if namespace.Name == "tyrs_hand" && tool.Name == "automation_update" {
				found = true
			}
		}
	}
	require.True(t, found)
	require.Contains(t, string(params), `"name":"generate_image"`)
}

func TestDesktopResumeDoesNotInjectNewDynamicTools(t *testing.T) {
	controller := &desktopController{processor: &Processor{}, workspace: &workspaceCodex{}}
	params := controller.configureDesktopThreadRuntime(appserverhub.Call{
		Role: appserverhub.RoleDesktop, Method: "thread/resume",
	}, json.RawMessage(`{"threadId":"thread-1","cwd":"/tmp/project"}`))
	var value struct {
		DynamicTools []json.RawMessage `json:"dynamicTools"`
	}
	require.NoError(t, json.Unmarshal(params, &value))
	require.Empty(t, value.DynamicTools)
}

func TestGitHubWorkItemDoesNotReceiveImageGenerationTool(t *testing.T) {
	specs := githubWorkItemTools(config.Config{}, ports.DynamicToolSpec{
		Type: "namespace", Name: "github",
	})
	require.False(t, hasDynamicTool(specs, "", "generate_image"))
}

func TestAutomationSpecRequiresOnlyActionAtSchemaBoundary(t *testing.T) {
	spec := automationSpec()
	require.Equal(t, "tyrs_hand", spec.Name)
	require.Len(t, spec.Tools, 1)
	require.Equal(t, "automation_update", spec.Tools[0].Name)
	var schema struct {
		Required             []string                   `json:"required"`
		AdditionalProperties bool                       `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(spec.Tools[0].InputSchema, &schema))
	require.Equal(t, []string{"action"}, schema.Required)
	require.False(t, schema.AdditionalProperties)
	for _, property := range []string{"task_id", "kind", "name", "prompt", "schedule",
		"timezone", "status", "settings", "include_deleted"} {
		require.Contains(t, schema.Properties, property)
	}
	var kind struct {
		Default     string `json:"default"`
		Description string `json:"description"`
	}
	require.NoError(t, json.Unmarshal(schema.Properties["kind"], &kind))
	require.Equal(t, "heartbeat", kind.Default)
	require.Contains(t, kind.Description, "Defaults to heartbeat")
	require.Contains(t, spec.Tools[0].Description, "Create heartbeat tasks by default")
}

func TestScheduledRunAddsUnattendedDeveloperInstruction(t *testing.T) {
	task := &workerprotocol.Task{}
	task.Claimed.ActorLogin = scheduledTaskActorLogin
	result := workspaceDeveloperInstructions(task, "现有开发者指令")
	require.Contains(t, result, "现有开发者指令")
	require.Contains(t, result, "无人值守定时任务")
	require.Contains(t, result, "避免请求额外输入")

	task.Claimed.ActorLogin = "ordinary-user"
	require.Equal(t, "现有开发者指令",
		workspaceDeveloperInstructions(task, "现有开发者指令"))
}

func hasDynamicTool(specs []ports.DynamicToolSpec, namespace, name string) bool {
	for _, spec := range specs {
		if namespace == "" && spec.Type == "function" && spec.Name == name {
			return true
		}
		if spec.Name != namespace {
			continue
		}
		for _, tool := range spec.Tools {
			if tool.Name == name {
				return true
			}
		}
	}
	return false
}
