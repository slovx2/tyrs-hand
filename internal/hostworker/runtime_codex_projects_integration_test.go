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

func TestRuntimeCodexProjectsRealSSH(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "codex-projects")
}

type nativeProject struct {
	ID, Name string
	Roots    []struct{ Path string }
	Metadata map[string]string
	Position int64
}

type nativeProjectResult struct{ Project nativeProject }

func nativeProjects(t *testing.T, ctx context.Context, client *codex.SocketClient) []nativeProject {
	t.Helper()
	var cursor *string
	projects := []nativeProject{}
	seen := map[string]bool{}
	for page := 0; page < 10; page++ {
		result := nativeMetadataCall[struct {
			Data       []nativeProject
			NextCursor *string
		}](t, ctx, client,
			"project/list", map[string]any{"limit": 1, "cursor": cursor, "sortKey": "position", "sortDirection": "asc"})
		require.LessOrEqual(t, len(result.Data), 1)
		for _, project := range result.Data {
			require.NotEmpty(t, project.ID)
			require.False(t, seen[project.ID], "原生项目分页不能重复")
			seen[project.ID] = true
			projects = append(projects, project)
		}
		cursor = result.NextCursor
		if cursor == nil {
			break
		}
	}
	require.Nil(t, cursor, "原生项目分页必须收敛")
	return projects
}

func awaitNativeProjectNotice(t *testing.T, ctx context.Context, events *codex.EventSubscription, id, change string) {
	t.Helper()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("未收到原生项目变更通知")
		case event, ok := <-events.Events():
			require.True(t, ok, "项目通知流不能提前关闭")
			if event.Method != "project/changed" {
				continue
			}
			var value struct{ ProjectID, ChangeType string }
			require.NoError(t, json.Unmarshal(event.Params, &value))
			if value.ProjectID == id && value.ChangeType == change {
				return
			}
		}
	}
}

func awaitNativeThreadProject(t *testing.T, ctx context.Context, events *codex.EventSubscription, threadID string, projectID *string) {
	t.Helper()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("未收到原生线程项目归属变更")
		case event, ok := <-events.Events():
			require.True(t, ok)
			if event.Method != "thread/project/updated" {
				continue
			}
			var value struct {
				ThreadID  string
				ProjectID *string
			}
			require.NoError(t, json.Unmarshal(event.Params, &value))
			require.Equal(t, threadID, value.ThreadID)
			require.Equal(t, projectID, value.ProjectID)
			return
		}
	}
}

// PROJECT-001：真实原生项目、幂等创建、分页重排、线程迁移、重启和删除后的历史归属。
func verifyCodexNativeProjects(t *testing.T, ctx context.Context, client *codex.SocketClient, root string, registry *RuntimeRegistry, connection *ssh.Client) {
	t.Helper()
	workspace := filepath.Join(root, "project")
	additional := filepath.Join(root, "additional-project")
	require.NoError(t, os.MkdirAll(additional, 0o700))
	sentinel := filepath.Join(additional, "retain.txt")
	require.NoError(t, os.WriteFile(sentinel, []byte("项目元数据操作不得改写文件"), 0o600))
	events := client.Subscribe(codex.ThreadFilter{})
	defer events.Close()
	require.Empty(t, nativeProjects(t, ctx, client))
	create := map[string]any{"name": "原项目", "roots": []map[string]string{{"path": workspace}},
		"metadata": map[string]string{"marker": "PROJECT-001"}, "idempotencyKey": "project-original"}
	first := nativeMetadataCall[nativeProjectResult](t, ctx, client, "project/create", create).Project
	require.Equal(t, "原项目", first.Name)
	require.Equal(t, "PROJECT-001", first.Metadata["marker"])
	awaitNativeProjectNotice(t, ctx, events, first.ID, "created")
	repeated := nativeMetadataCall[nativeProjectResult](t, ctx, client, "project/create", create).Project
	require.Equal(t, first.ID, repeated.ID, "相同幂等键不能创建第二个项目")
	second := nativeMetadataCall[nativeProjectResult](t, ctx, client, "project/create", map[string]any{
		"name": "第二项目", "roots": []map[string]string{{"path": additional}}, "idempotencyKey": "project-second"}).Project
	awaitNativeProjectNotice(t, ctx, events, second.ID, "created")
	require.Len(t, nativeProjects(t, ctx, client), 2)
	require.NoError(t, client.Call(ctx, "project/move", map[string]any{"projectId": second.ID, "beforeProjectId": first.ID}, nil))
	require.Equal(t, second.ID, nativeProjects(t, ctx, client)[0].ID)
	updated := nativeMetadataCall[nativeProjectResult](t, ctx, client, "project/update", map[string]any{
		"projectId": first.ID, "name": "重命名项目", "roots": []map[string]string{{"path": workspace}, {"path": additional}},
		"metadata": map[string]string{"marker": "PROJECT-001-updated"}}).Project
	require.Equal(t, "重命名项目", updated.Name)
	require.Len(t, updated.Roots, 2)
	awaitNativeProjectNotice(t, ctx, events, first.ID, "updated")
	thread := readSessionThread(t, ctx, client, "thread/start", map[string]any{"cwd": workspace,
		"projectId": first.ID, "sandbox": "danger-full-access", "approvalPolicy": "never", "historyMode": "paginated"})
	runNativeMetadataTurn(t, ctx, client, thread.ID, "PROJECT_NATIVE_HISTORY")
	readMembership := func() *string {
		return nativeMetadataCall[struct{ Thread struct{ ProjectID *string } }](t, ctx, client, "thread/read",
			map[string]any{"threadId": thread.ID}).Thread.ProjectID
	}
	require.Equal(t, &first.ID, readMembership(), "创建回合必须实际保留所选项目归属")
	membershipEvents := client.Subscribe(codex.ThreadFilter{ThreadID: thread.ID})
	defer membershipEvents.Close()
	importParams := map[string]any{"name": "导入项目", "roots": []map[string]string{{"path": workspace}},
		"threads": []string{thread.ID}, "idempotencyKey": "project-imported"}
	imported := nativeMetadataCall[nativeProjectResult](t, ctx, client, "project/import", importParams).Project
	require.NotEqual(t, first.ID, imported.ID)
	awaitNativeProjectNotice(t, ctx, events, imported.ID, "created")
	awaitNativeThreadProject(t, ctx, membershipEvents, thread.ID, &imported.ID)
	require.Equal(t, imported.ID, nativeMetadataCall[nativeProjectResult](t, ctx, client, "project/import", importParams).Project.ID)
	require.Equal(t, &imported.ID, readMembership())
	var listed struct{ Data []struct{ ID string } }
	require.NoError(t, client.Call(ctx, "thread/list", map[string]any{"projectId": imported.ID}, &listed))
	require.Len(t, listed.Data, 1)
	require.Equal(t, thread.ID, listed.Data[0].ID)
	require.NoError(t, client.Call(ctx, "thread/metadata/update", map[string]any{"threadId": thread.ID, "projectId": second.ID}, nil))
	awaitNativeThreadProject(t, ctx, membershipEvents, thread.ID, &second.ID)
	require.Equal(t, &second.ID, readMembership())
	require.NoError(t, client.Call(ctx, "thread/list", map[string]any{"projectId": imported.ID}, &listed))
	require.Empty(t, listed.Data, "迁移后旧项目不能继续认领线程")
	beforeRestart := nativeProjects(t, ctx, client)
	otherGeneration := registry.entries[runtimeidentity.Claude].Runtime.Generation()
	require.NoError(t, registry.Restart(runtimeidentity.Codex))
	client = connectRuntimeSSH(t, ctx, connection, runtimeidentity.Codex)
	require.Equal(t, otherGeneration, registry.entries[runtimeidentity.Claude].Runtime.Generation())
	retained := nativeMetadataCall[nativeProjectResult](t, ctx, client, "project/read", map[string]any{"projectId": first.ID}).Project
	require.Equal(t, updated.Name, retained.Name)
	require.Equal(t, updated.Metadata, retained.Metadata)
	require.Equal(t, updated.Roots, retained.Roots)
	require.Len(t, beforeRestart, 3)
	require.Equal(t, beforeRestart, nativeProjects(t, ctx, client), "重启后项目内容和排序保持不变")
	require.Equal(t, &second.ID, readMembership())
	require.Len(t, readSessionThread(t, ctx, client, "thread/read", map[string]any{"threadId": thread.ID, "includeTurns": true}).Turns, 1)
	// 重启后的只读历史不会加载会话；先真实恢复会话，再验收实时归属通知。
	readSessionThread(t, ctx, client, "thread/resume", map[string]any{"threadId": thread.ID})
	deletionEvents := client.Subscribe(codex.ThreadFilter{})
	defer deletionEvents.Close()
	deletedMembership := client.Subscribe(codex.ThreadFilter{ThreadID: thread.ID})
	defer deletedMembership.Close()
	for _, id := range []string{first.ID, imported.ID, second.ID} {
		require.NoError(t, client.Call(ctx, "project/delete", map[string]any{"projectId": id}, nil))
		awaitNativeProjectNotice(t, ctx, deletionEvents, id, "deleted")
		if id != second.ID {
			require.Equal(t, &second.ID, readMembership(), "删除无关项目不能解除当前归属")
		}
	}
	awaitNativeThreadProject(t, ctx, deletedMembership, thread.ID, nil)
	require.NoError(t, registry.Restart(runtimeidentity.Codex))
	client = connectRuntimeSSH(t, ctx, connection, runtimeidentity.Codex)
	require.Empty(t, nativeProjects(t, ctx, client))
	require.Nil(t, readMembership(), "删除项目应移除关联，不得删除会话")
	require.Len(t, readSessionThread(t, ctx, client, "thread/read", map[string]any{"threadId": thread.ID, "includeTurns": true}).Turns, 1)
	data, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	require.Equal(t, "项目元数据操作不得改写文件", string(data))
}
