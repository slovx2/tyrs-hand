package devcontainer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type scriptedCommandResult struct {
	output string
	err    error
}

type scriptedCommandRunner struct {
	t          *testing.T
	results    []scriptedCommandResult
	commands   [][]string
	nextResult int
}

func (r *scriptedCommandRunner) Run(_ context.Context, _ []string, _ string,
	arguments ...string,
) (string, error) {
	r.t.Helper()
	r.commands = append(r.commands, append([]string(nil), arguments...))
	if r.nextResult >= len(r.results) {
		r.t.Fatalf("收到未配置结果的命令: %v", arguments)
	}
	result := r.results[r.nextResult]
	r.nextResult++
	return result.output, result.err
}

func browserRuntimeManager(t *testing.T, results ...scriptedCommandResult) (*Manager, *scriptedCommandRunner) {
	runner := &scriptedCommandRunner{t: t, results: results}
	return &Manager{dockerBin: "docker", dockerHost: "inherit", runner: runner}, runner
}

func TestContainerIPHandlesInspectResults(t *testing.T) {
	runtime := Runtime{Container: "dev-1"}
	t.Run("返回第一个地址", func(t *testing.T) {
		manager, runner := browserRuntimeManager(t, scriptedCommandResult{output: "\n172.18.0.3\n172.19.0.4"})
		address, err := manager.ContainerIP(context.Background(), runtime)
		require.NoError(t, err)
		require.Equal(t, "172.18.0.3", address)
		require.Contains(t, strings.Join(runner.commands[0], " "), "inspect --format")
	})
	t.Run("Docker 检查失败", func(t *testing.T) {
		manager, _ := browserRuntimeManager(t, scriptedCommandResult{err: errors.New("inspect failed")})
		_, err := manager.ContainerIP(context.Background(), runtime)
		require.ErrorContains(t, err, "inspect failed")
	})
	t.Run("容器没有地址", func(t *testing.T) {
		manager, _ := browserRuntimeManager(t, scriptedCommandResult{output: " \n"})
		_, err := manager.ContainerIP(context.Background(), runtime)
		require.ErrorContains(t, err, "没有可用的 IPv4")
	})
}

func TestExportWorkspaceFileValidatesEveryBoundary(t *testing.T) {
	runtime := Runtime{Container: "dev-1", Workspace: "/workspace/repo"}
	t.Run("拒绝越界路径", func(t *testing.T) {
		manager, runner := browserRuntimeManager(t)
		err := manager.ExportWorkspaceFile(context.Background(), runtime, "/workspace/other/file", "/tmp/file")
		require.ErrorContains(t, err, "不在当前工作区")
		require.Empty(t, runner.commands)
	})
	t.Run("拒绝符号链接和不存在路径", func(t *testing.T) {
		manager, _ := browserRuntimeManager(t, scriptedCommandResult{output: "/outside/file"})
		err := manager.ExportWorkspaceFile(context.Background(), runtime, "/workspace/repo/file", "/tmp/file")
		require.ErrorContains(t, err, "不存在或包含符号链接")
	})
	t.Run("返回 stat 错误", func(t *testing.T) {
		manager, _ := browserRuntimeManager(t,
			scriptedCommandResult{output: "/workspace/repo/file"},
			scriptedCommandResult{err: errors.New("stat failed")})
		err := manager.ExportWorkspaceFile(context.Background(), runtime, "/workspace/repo/file", "/tmp/file")
		require.ErrorContains(t, err, "stat failed")
	})
	for _, metadata := range []string{"directory:12", "regular file", "regular file:not-a-size", "regular file:26214401"} {
		metadata := metadata
		t.Run("拒绝元数据_"+strings.ReplaceAll(metadata, ":", "_"), func(t *testing.T) {
			manager, _ := browserRuntimeManager(t,
				scriptedCommandResult{output: "/workspace/repo/file"},
				scriptedCommandResult{output: metadata})
			err := manager.ExportWorkspaceFile(context.Background(), runtime, "/workspace/repo/file", "/tmp/file")
			require.Error(t, err)
		})
	}
	t.Run("复制普通文件", func(t *testing.T) {
		manager, runner := browserRuntimeManager(t,
			scriptedCommandResult{output: "/workspace/repo/file"},
			scriptedCommandResult{output: "regular file:12"},
			scriptedCommandResult{})
		require.NoError(t, manager.ExportWorkspaceFile(context.Background(), runtime,
			"/workspace/repo/file", "/tmp/file"))
		require.Contains(t, strings.Join(runner.commands[2], " "), "cp dev-1:/workspace/repo/file /tmp/file")
	})
}

func TestImportWorkspaceFileHandlesCopyFailuresAndSuccess(t *testing.T) {
	runtime := Runtime{Container: "dev-1", Workspace: "/workspace/repo", UID: 1001, GID: 1002}
	t.Run("拒绝工作区根和越界路径", func(t *testing.T) {
		manager, runner := browserRuntimeManager(t)
		for _, destination := range []string{"/workspace/repo", "/workspace/other/file"} {
			err := manager.ImportWorkspaceFile(context.Background(), runtime, "/tmp/file", destination)
			require.ErrorContains(t, err, "不在当前工作区")
		}
		require.Empty(t, runner.commands)
	})
	t.Run("拒绝符号链接目录", func(t *testing.T) {
		manager, _ := browserRuntimeManager(t, scriptedCommandResult{output: "/outside"})
		err := manager.ImportWorkspaceFile(context.Background(), runtime, "/tmp/file", "/workspace/repo/out/file")
		require.ErrorContains(t, err, "包含符号链接")
	})
	t.Run("返回创建目录错误", func(t *testing.T) {
		manager, _ := browserRuntimeManager(t,
			scriptedCommandResult{output: "/workspace/repo/out"},
			scriptedCommandResult{err: errors.New("mkdir failed")})
		err := manager.ImportWorkspaceFile(context.Background(), runtime, "/tmp/file", "/workspace/repo/out/file")
		require.ErrorContains(t, err, "mkdir failed")
	})
	t.Run("返回复制错误", func(t *testing.T) {
		manager, _ := browserRuntimeManager(t,
			scriptedCommandResult{output: "/workspace/repo/out"}, scriptedCommandResult{},
			scriptedCommandResult{err: errors.New("copy failed")})
		err := manager.ImportWorkspaceFile(context.Background(), runtime, "/tmp/file", "/workspace/repo/out/file")
		require.ErrorContains(t, err, "copy failed")
	})
	t.Run("返回改属主错误", func(t *testing.T) {
		manager, _ := browserRuntimeManager(t,
			scriptedCommandResult{output: "/workspace/repo/out"}, scriptedCommandResult{},
			scriptedCommandResult{}, scriptedCommandResult{err: errors.New("chown failed")})
		err := manager.ImportWorkspaceFile(context.Background(), runtime, "/tmp/file", "/workspace/repo/out/file")
		require.ErrorContains(t, err, "chown failed")
	})
	t.Run("复制并设置属主", func(t *testing.T) {
		manager, runner := browserRuntimeManager(t,
			scriptedCommandResult{output: "/workspace/repo/out"}, scriptedCommandResult{},
			scriptedCommandResult{}, scriptedCommandResult{})
		require.NoError(t, manager.ImportWorkspaceFile(context.Background(), runtime,
			"/tmp/file", "/workspace/repo/out/file"))
		require.Contains(t, strings.Join(runner.commands[1], " "), "--user 1001:1002")
		require.Contains(t, strings.Join(runner.commands[3], " "), "chown 1001:1002")
	})
}
