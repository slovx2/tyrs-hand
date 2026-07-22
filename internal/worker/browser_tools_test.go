package worker

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/stretchr/testify/require"
)

func TestBrowserFileExchangeStaysInsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	exchange := t.TempDir()
	hostRoot := "/opt/tyrs-hand/browser-files"
	cfg := config.Config{BrowserMCPURL: "http://host.docker.internal:8931/mcp",
		BrowserFilesRoot: exchange, BrowserFilesHostRoot: hostRoot}
	source := filepath.Join(workspace, "report.txt")
	require.NoError(t, os.WriteFile(source, []byte("result"), 0o600))

	result, err := executeBrowserTool(context.Background(), cfg, "task-id", workspace,
		nil, nil, codex.ToolCallRequest{Tool: "stage_file",
			Arguments: json.RawMessage(`{"source":"report.txt"}`)})
	require.NoError(t, err)
	require.True(t, result.Success)
	var staged stagedBrowserFile
	require.NoError(t, json.Unmarshal([]byte(result.ContentItems[0].Text), &staged))
	require.Contains(t, staged.HostPath, hostRoot)
	require.Len(t, staged.SHA256, 64)

	link := filepath.Join(workspace, "link.txt")
	require.NoError(t, os.Symlink(source, link))
	_, err = executeBrowserTool(context.Background(), cfg, "task-id", workspace,
		nil, nil, codex.ToolCallRequest{Tool: "stage_file",
			Arguments: json.RawMessage(`{"source":"link.txt"}`)})
	require.ErrorContains(t, err, "符号链接")

	_, err = executeBrowserTool(context.Background(), cfg, "task-id", workspace,
		nil, nil, codex.ToolCallRequest{Tool: "stage_file",
			Arguments: json.RawMessage(`{"source":"../outside.txt"}`)})
	require.Error(t, err)
}

func TestBrowserDownloadImport(t *testing.T) {
	workspace := t.TempDir()
	exchange := t.TempDir()
	cfg := config.Config{BrowserMCPURL: "http://host.docker.internal:8931/mcp",
		BrowserFilesRoot: exchange, BrowserFilesHostRoot: "/opt/tyrs-hand/browser-files"}
	download := filepath.Join(exchange, "download.txt")
	require.NoError(t, os.WriteFile(download, []byte("download"), 0o644))

	result, err := executeBrowserTool(context.Background(), cfg, "task-id", workspace,
		nil, nil, codex.ToolCallRequest{Tool: "import_download",
			Arguments: json.RawMessage(`{"source":"` + download + `","destination":"artifacts/download.txt"}`)})
	require.NoError(t, err)
	require.True(t, result.Success)
	data, err := os.ReadFile(filepath.Join(workspace, "artifacts", "download.txt"))
	require.NoError(t, err)
	require.Equal(t, "download", string(data))
}

func TestBrowserFileExchangeRejectsUnsafeDownloadsAndDestinations(t *testing.T) {
	workspace := t.TempDir()
	exchange := t.TempDir()
	hostRoot := "/opt/tyrs-hand/browser-files"
	cfg := config.Config{BrowserMCPURL: "http://host.docker.internal:8931/mcp",
		BrowserFilesRoot: exchange, BrowserFilesHostRoot: hostRoot}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o644))

	_, err := importBrowserDownload(context.Background(), cfg, workspace, nil, nil,
		outside, "download.txt")
	require.ErrorContains(t, err, "交换目录")
	link := filepath.Join(exchange, "link.txt")
	require.NoError(t, os.Symlink(outside, link))
	_, err = importBrowserDownload(context.Background(), cfg, workspace, nil, nil,
		link, "download.txt")
	require.ErrorContains(t, err, "普通文件")

	large := filepath.Join(exchange, "large.bin")
	require.NoError(t, os.WriteFile(large, nil, 0o644))
	require.NoError(t, os.Truncate(large, browserFileLimit+1))
	_, err = importBrowserDownload(context.Background(), cfg, workspace, nil, nil,
		large, "download.bin")
	require.ErrorContains(t, err, "25 MiB")

	download := filepath.Join(exchange, "download.txt")
	require.NoError(t, os.WriteFile(download, []byte("download"), 0o644))
	_, err = importBrowserDownload(context.Background(), cfg, workspace, nil, nil,
		download, "../escape.txt")
	require.ErrorContains(t, err, "工作区")
	linkedDirectory := filepath.Join(workspace, "linked")
	require.NoError(t, os.Symlink(t.TempDir(), linkedDirectory))
	_, err = importBrowserDownload(context.Background(), cfg, workspace, nil, nil,
		download, "linked/download.txt")
	require.ErrorContains(t, err, "符号链接")

	hostPath := filepath.Join(hostRoot, "nested", "download.txt")
	workerPath, err := browserSourcePath(cfg, hostPath)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(exchange, "nested", "download.txt"), workerPath)
}

func TestBrowserToolsRejectInvalidCallsAndProbePorts(t *testing.T) {
	disabled := config.Config{}
	_, err := executeBrowserTool(context.Background(), disabled, "task", t.TempDir(), nil, nil,
		codex.ToolCallRequest{Tool: "stage_file", Arguments: json.RawMessage(`{}`)})
	require.ErrorContains(t, err, "未配置")

	cfg := config.Config{BrowserMCPURL: "http://host.docker.internal:8931/mcp"}
	_, err = executeBrowserTool(context.Background(), cfg, "task", t.TempDir(), nil, nil,
		codex.ToolCallRequest{Tool: "missing", Arguments: json.RawMessage(`{}`)})
	require.ErrorContains(t, err, "不存在")
	_, err = executeBrowserTool(context.Background(), cfg, "task", t.TempDir(), nil, nil,
		codex.ToolCallRequest{Tool: "stage_file", Arguments: json.RawMessage(`{"source":`)})
	require.Error(t, err)
	require.Error(t, probePort(context.Background(), "127.0.0.1", 0))

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, probePort(context.Background(), "127.0.0.1", port))
	_ = listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = probePort(ctx, "127.0.0.1", port)
	require.ErrorContains(t, err, "监听 0.0.0.0")
}

func TestBrowserTaskCleanupAndSweeper(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{BrowserMCPURL: "http://host.docker.internal:8931/mcp",
		BrowserFilesRoot: root}
	taskDirectory := filepath.Join(root, "task-1")
	require.NoError(t, os.MkdirAll(taskDirectory, 0o755))
	cleanupBrowserTask(cfg, "task-1")
	_, err := os.Stat(taskDirectory)
	require.ErrorIs(t, err, os.ErrNotExist)

	old := filepath.Join(root, "old-task")
	fresh := filepath.Join(root, "fresh-task")
	require.NoError(t, os.MkdirAll(old, 0o755))
	require.NoError(t, os.MkdirAll(fresh, 0o755))
	oldTime := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(old, oldTime, oldTime))
	sweepBrowserFiles(root)
	_, err = os.Stat(old)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(fresh)
	require.NoError(t, err)
}
