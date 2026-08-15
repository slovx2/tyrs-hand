package worker

import (
	"encoding/json"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
)

func TestAutomationSpecIsInjectedForWorkspaceProjects(t *testing.T) {
	directory := workspaceGitTools(&workerprotocol.WorkspaceProjectContext{
		WorkspaceKind: "directory",
	})
	require.Len(t, directory, 1)
	require.True(t, hasDynamicTool(directory, "tyrs_hand", "automation_update"))
	require.False(t, hasDynamicTool(directory, "git", "status"))

	git := workspaceGitTools(&workerprotocol.WorkspaceProjectContext{
		WorkspaceKind: "git", CloneURL: "https://example.invalid/repository.git",
	})
	require.True(t, hasDynamicTool(git, "tyrs_hand", "automation_update"))
	require.True(t, hasDynamicTool(git, "git", "status"))
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
