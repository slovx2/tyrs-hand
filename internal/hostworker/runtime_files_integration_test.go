//go:build integration

package hostworker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/stretchr/testify/require"
)

// 从真实 SSH 执行文件协议，并用宿主文件内容核对副作用。
func verifyRuntimeFilesystem(t *testing.T, ctx context.Context, client *codex.SocketClient, root string) {
	t.Helper()
	var result map[string]any
	call := func(method string, params map[string]any) {
		t.Helper()
		result = nil
		require.NoError(t, client.Call(ctx, method, params, &result), method)
	}
	dir := filepath.Join(root, "文件协议")
	call("fs/createDirectory", map[string]any{"path": dir})
	path := filepath.Join(dir, "binary.dat")
	content := []byte{0, 1, 2, 127, 128, 255, '\n'}
	encoded := base64.StdEncoding.EncodeToString(content)
	call("fs/writeFile", map[string]any{"path": path, "dataBase64": encoded})
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, content, actual)
	call("fs/readFile", map[string]any{"path": path})
	require.Equal(t, encoded, result["dataBase64"])
	for _, query := range []string{"binary", "bnrd"} {
		call("fuzzyFileSearch", map[string]any{"query": query, "roots": []string{dir}, "cancellationToken": nil})
		files, ok := result["files"].([]any)
		require.True(t, ok)
		matched := false
		for _, value := range files {
			file := value.(map[string]any)
			if file["path"] != "binary.dat" {
				continue
			}
			matched = true
			require.Equal(t, dir, file["root"])
			require.Equal(t, "binary.dat", file["file_name"])
			require.Equal(t, "file", file["match_type"])
			require.Greater(t, file["score"].(float64), float64(0))
		}
		require.True(t, matched, "模糊文件搜索必须支持非连续字符: query=%s root=%s result=%v", query, dir, files)
	}
	call("fs/readDirectory", map[string]any{"path": dir})
	require.Equal(t, []any{map[string]any{"fileName": "binary.dat", "isFile": true, "isDirectory": false}}, result["entries"])
	call("fs/getMetadata", map[string]any{"path": path})
	require.Equal(t, true, result["isFile"])
	for _, key := range []string{"createdAtMs", "modifiedAtMs"} {
		value := result[key].(float64)
		require.Equal(t, float64(int64(value)), value, "%s 必须为 schema 规定的整数", key)
	}
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(path, link))
	call("fs/getMetadata", map[string]any{"path": link})
	require.Equal(t, true, result["isSymlink"])
	require.Equal(t, true, result["isFile"], "文件类型跟随符号链接")
	copyPath := filepath.Join(dir, "copied.dat")
	call("fs/copy", map[string]any{"sourcePath": path, "destinationPath": copyPath})
	actual, err = os.ReadFile(copyPath)
	require.NoError(t, err)
	require.Equal(t, content, actual)
	require.Error(t, client.Call(ctx, "fs/copy", map[string]any{"sourcePath": dir, "destinationPath": dir + "-copy"}, &result))
	call("fs/copy", map[string]any{"sourcePath": dir, "destinationPath": dir + "-copy", "recursive": true})
	actual, err = os.ReadFile(filepath.Join(dir+"-copy", "binary.dat"))
	require.NoError(t, err)
	require.Equal(t, content, actual)

	events := client.Subscribe(codex.ThreadFilter{})
	defer events.Close()
	call("fs/watch", map[string]any{"path": path, "watchId": "same-watch"})
	require.NoError(t, os.WriteFile(path, []byte("watch changed"), 0o600))
	select {
	case event := <-events.Events():
		require.Equal(t, "fs/changed", event.Method)
		var params struct {
			WatchID      string   `json:"watchId"`
			ChangedPaths []string `json:"changedPaths"`
		}
		require.NoError(t, json.Unmarshal(event.Params, &params))
		require.Equal(t, "same-watch", params.WatchID)
		require.Contains(t, params.ChangedPaths, path, "监视文件不能生成 file/file 路径")
	case <-time.After(3 * time.Second):
		t.Fatal("没有收到文件变化事件")
	}
	call("fs/unwatch", map[string]any{"watchId": "same-watch"})
	call("fs/remove", map[string]any{"path": dir})
	call("fs/remove", map[string]any{"path": dir + "-copy"})
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err))
	require.Error(t, client.Call(ctx, "fs/readFile", map[string]any{"path": path}, &result))
	require.Error(t, client.Call(ctx, "fs/remove", map[string]any{"path": path, "force": false}, &result))
}

func verifyRuntimeWatchIsolation(t *testing.T, ctx context.Context, first, second *codex.SocketClient, root string) {
	t.Helper()
	clients := []*codex.SocketClient{first, second}
	events := make([]*codex.EventSubscription, 2)
	paths := []string{filepath.Join(root, "watch-a"), filepath.Join(root, "watch-b")}
	for index, client := range clients {
		require.NoError(t, os.MkdirAll(paths[index], 0o700))
		events[index] = client.Subscribe(codex.ThreadFilter{})
		defer events[index].Close()
		var result any
		require.NoError(t, client.Call(ctx, "fs/watch", map[string]any{"path": paths[index], "watchId": "same-id"}, &result))
	}
	for index, dir := range paths {
		file := filepath.Join(dir, "changed.txt")
		started := time.Now()
		require.NoError(t, os.WriteFile(file, []byte("owned"), 0o600))
		received := false
		// 与单监听用例使用相同的有界窗口；目录刚建立的事件不能代替目标文件变化。
		deadline := time.After(3 * time.Second)
		assertOwned := func(event codex.Event, owner int) bool {
			t.Helper()
			require.Equal(t, "fs/changed", event.Method)
			var params struct {
				WatchID      string   `json:"watchId"`
				ChangedPaths []string `json:"changedPaths"`
			}
			require.NoError(t, json.Unmarshal(event.Params, &params))
			require.Equal(t, "same-id", params.WatchID)
			matched := false
			for _, changed := range params.ChangedPaths {
				require.True(t, changed == paths[owner] || strings.HasPrefix(changed, paths[owner]+string(os.PathSeparator)),
					"文件变化泄漏到其他连接: %s", changed)
				matched = matched || changed == file
			}
			return matched
		}
	collect:
		for {
			select {
			case event := <-events[index].Events():
				if assertOwned(event, index) && !received {
					received = true
					t.Logf("监听 %d 在 %s 收到目标文件变化", index, time.Since(started))
				}
			case event := <-events[1-index].Events():
				assertOwned(event, 1-index)
			case <-deadline:
				break collect
			}
		}
		require.True(t, received, "同名 watch ID 不能覆盖其他客户端的 watch")
		var result any
		require.NoError(t, clients[index].Call(ctx, "fs/unwatch", map[string]any{"watchId": "same-id"}, &result))
	}
}
