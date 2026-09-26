//go:build integration

package hostworker

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/mobile/sshtransport"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestRuntimeMobileSFTPRealSSHBothEngines(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "mobile-sftp")
}

// FILES-001 在同一双引擎运行中验证文件 RPC、watch、进程和真实手机传输。
// 复用的专项逐项检查宿主副作用；传输还必须经过真实 CLI 工具和结果续写。
func TestRuntimeFilesRealSSHBothEngines(t *testing.T) {
	testRuntimeRegistryRealSSH(t, "files-acceptance")
}

type runtimeSFTPFixture struct {
	root    string
	content []byte
	calls   map[runtimeidentity.Engine]*atomic.Int64
}

func newRuntimeSFTPFixture(root string) *runtimeSFTPFixture {
	content := make([]byte, 256)
	for index := range content {
		content[index] = byte(index)
	}
	return &runtimeSFTPFixture{root: root, content: append(content, []byte("真实附件原始字节\n")...),
		calls: map[runtimeidentity.Engine]*atomic.Int64{runtimeidentity.Codex: {}, runtimeidentity.Claude: {}}}
}

func (f *runtimeSFTPFixture) paths(engine runtimeidentity.Engine) (string, string) {
	digest := sha256.Sum256(f.content)
	return filepath.Join(f.root, string(engine), ".cache", "tyrs-hand", "attachments", hex.EncodeToString(digest[:])),
		filepath.Join(f.root, "project", string(engine)+"-native-binary.dat")
}

func (f *runtimeSFTPFixture) model(t *testing.T, w http.ResponseWriter, request *http.Request, engine runtimeidentity.Engine, body []byte) {
	step := f.calls[engine].Add(1)
	if step == 2 {
		// 检查真实工具输出，不能把模型输入中出现的文件路径当作已读取。
		expected := hex.EncodeToString(f.content)
		if engine == runtimeidentity.Claude {
			requireGoalToolResult(t, body, "sftp-native-tool", expected)
		} else {
			var input struct {
				Input []struct {
					Type   string
					CallID string `json:"call_id"`
					Output json.RawMessage
				}
			}
			require.NoError(t, json.Unmarshal(body, &input))
			found := false
			for _, item := range input.Input {
				if item.Type == "function_call_output" && item.CallID == "sftp-native-tool" {
					require.Contains(t, string(item.Output), expected)
					found = true
				}
			}
			require.True(t, found, "Codex 必须把原生命令结果交回模型")
		}
		runtimeTextModel(w, request, "SFTP_NATIVE_BYTES_VERIFIED", "sftp-final")
		return
	}
	if step != 1 {
		t.Error("文件传输或重试触发额外模型请求")
		http.Error(w, "unexpected model call", http.StatusBadRequest)
		return
	}
	source, destination := f.paths(engine)
	command := "od -An -v -tx1 '" + source + "' | tr -d '[:space:]'; printf '\n'; cp '" + source + "' '" + destination + "'"
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(kind string, value map[string]any) {
		value["type"] = kind
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, encoded)
	}
	if engine == runtimeidentity.Codex {
		var input struct{ Tools []struct{ Name string } }
		require.NoError(t, json.Unmarshal(body, &input))
		name := ""
		for _, tool := range input.Tools {
			if tool.Name == "exec_command" || tool.Name == "shell_command" {
				name = tool.Name
				break
			}
		}
		require.NotEmpty(t, name, "固定 Codex 必须提供原生命令工具")
		args := map[string]any{"cmd": command, "workdir": filepath.Join(f.root, "project"), "max_output_tokens": 2000}
		if name == "shell_command" {
			args = map[string]any{"command": command, "workdir": filepath.Join(f.root, "project")}
		}
		encoded, err := json.Marshal(args)
		require.NoError(t, err)
		event("response.created", map[string]any{"response": map[string]any{"id": "sftp-native"}})
		event("response.output_item.done", map[string]any{"item": map[string]any{"type": "function_call", "call_id": "sftp-native-tool", "name": name, "arguments": string(encoded)}})
		event("response.completed", map[string]any{"response": map[string]any{"id": "sftp-native", "usage": map[string]int{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}})
		return
	}
	encoded, err := json.Marshal(map[string]any{"command": command, "description": "读取上传附件原始字节并生成下载文件"})
	require.NoError(t, err)
	event("message_start", map[string]any{"message": map[string]any{"id": "msg_sftp_native", "type": "message", "role": "assistant", "model": "claude-config-model", "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 10, "output_tokens": 1}}})
	event("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "tool_use", "id": "sftp-native-tool", "name": "Bash", "input": map[string]any{}}})
	event("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(encoded)}})
	event("content_block_stop", map[string]any{"index": 0})
	event("message_delta", map[string]any{"delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]int{"output_tokens": 5}})
	event("message_stop", map[string]any{})
}

func verifyRuntimeMobileSFTP(t *testing.T, ctx context.Context, registry *RuntimeRegistry, clients map[runtimeidentity.Engine]*codex.SocketClient, private ed25519.PrivateKey, fixture *runtimeSFTPFixture) {
	t.Helper()
	key, err := ssh.MarshalPrivateKey(private, "sftp-acceptance")
	require.NoError(t, err)
	privateKey := string(pem.EncodeToMemory(key))
	local := filepath.Join(fixture.root, "phone-upload.dat")
	require.NoError(t, os.WriteFile(local, fixture.content, 0o600))
	paths := map[runtimeidentity.Engine]string{}
	checks := map[string]any{}
	for _, engine := range []runtimeidentity.Engine{runtimeidentity.Codex, runtimeidentity.Claude} {
		entry := registry.entries[engine]
		address := entry.SSH.Addr().(*net.TCPAddr)
		host, port, fingerprint := address.IP.String(), address.Port, entry.SSH.HostKeyFingerprint()
		upload := func() string {
			t.Helper()
			result, uploadErr := sshtransport.UploadAttachment(host, port, "mobile", privateKey, "", fingerprint, local, "attachment.dat", "application/octet-stream")
			require.NoError(t, uploadErr)
			var uploaded struct{ RemotePath, SHA256 string }
			require.NoError(t, json.Unmarshal([]byte(result), &uploaded))
			digest := sha256.Sum256(fixture.content)
			require.Equal(t, hex.EncodeToString(digest[:]), uploaded.SHA256)
			return uploaded.RemotePath
		}
		source, destination := fixture.paths(engine)
		require.Equal(t, source, upload())
		paths[engine] = source
		actual, readErr := os.ReadFile(source)
		require.NoError(t, readErr)
		require.Equal(t, fixture.content, actual)
		info, statErr := os.Stat(source)
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		// 内容寻址缓存不能仅凭相同长度误认已完成上传。
		corrupt := []byte(strings.Repeat("x", len(fixture.content)))
		require.NoError(t, os.WriteFile(source, corrupt, 0o600))
		require.Equal(t, source, upload())
		actual, readErr = os.ReadFile(source)
		require.NoError(t, readErr)
		require.Equal(t, fixture.content, actual, "同长度损坏缓存必须重新上传，不得报告伪 SHA256")
		before, statErr := os.Stat(source)
		require.NoError(t, statErr)
		require.Equal(t, source, upload())
		after, statErr := os.Stat(source)
		require.NoError(t, statErr)
		require.Equal(t, before.ModTime(), after.ModTime(), "内容正确时可以复用已验证缓存")
		thread := readSessionThread(t, ctx, clients[engine], "thread/start", map[string]any{"cwd": filepath.Join(fixture.root, "project"), "approvalPolicy": "never", "sandbox": "danger-full-access"})
		events := clients[engine].Subscribe(codex.ThreadFilter{ThreadID: thread.ID})
		defer events.Close()
		turn := nativeMetadataCall[struct{ Turn struct{ ID string } }](t, ctx, clients[engine], "turn/start", map[string]any{"threadId": thread.ID, "input": []map[string]any{{"type": "text", "text": "SFTP_READ_AND_COPY_UPLOADED_BYTES"}}})
		waitSFTPTurn(t, ctx, events, turn.Turn.ID)
		waitSessionTurn(t, ctx, clients[engine], thread.ID, turn.Turn.ID)
		actual, readErr = os.ReadFile(destination)
		require.NoError(t, readErr)
		require.Equal(t, fixture.content, actual, "真实 CLI 必须生成完整二进制文件")
		download := filepath.Join(fixture.root, "phone-cache", string(engine), "download.dat")
		encoded, downloadErr := sshtransport.DownloadFile(host, port, "mobile", privateKey, "", fingerprint, destination, download)
		require.NoError(t, downloadErr)
		var downloaded struct {
			LocalPath string
			Size      int64
		}
		require.NoError(t, json.Unmarshal([]byte(encoded), &downloaded))
		require.Equal(t, int64(len(fixture.content)), downloaded.Size)
		actual, readErr = os.ReadFile(downloaded.LocalPath)
		require.NoError(t, readErr)
		require.Equal(t, fixture.content, actual)
		_, downloadErr = sshtransport.DownloadFile(host, port, "mobile", privateKey, "", fingerprint, destination+"-missing", download+"-missing")
		require.Error(t, downloadErr)
		_, statErr = os.Stat(download + "-missing")
		require.True(t, os.IsNotExist(statErr), "失败下载不得暴露最终缓存文件")
		leftovers, globErr := filepath.Glob(download + "*.tmp")
		require.NoError(t, globErr)
		require.Empty(t, leftovers)
		verifyRuntimeSFTPFailures(t, entry, privateKey, fixture.root)
		digest := sha256.Sum256(actual)
		checks[string(engine)] = map[string]any{"sha256": hex.EncodeToString(digest[:]), "bytes": len(actual), "modelCalls": fixture.calls[engine].Load(), "nativeToolEffect": true, "corruptCacheRepaired": true, "missingDownloadRejected": true, "partialUploadNotPublished": true, "partialDownloadNotPublished": true, "interruptedTransferRetried": true, "oversizedRejected": true}
	}
	require.NotEqual(t, paths[runtimeidentity.Codex], paths[runtimeidentity.Claude], "两个入口的附件缓存必须隔离")
	if directory := os.Getenv("PROTOCOL_ARTIFACT_DIR"); directory != "" {
		encoded, encodeErr := json.MarshalIndent(map[string]any{"runId": os.Getenv("PROTOCOL_RUN_ID"), "caseName": t.Name(), "checks": checks}, "", "  ")
		require.NoError(t, encodeErr)
		require.NoError(t, os.WriteFile(filepath.Join(directory, "mobile-sftp-side-effects.json"), encoded, 0o600))
	}
}

func waitSFTPTurn(t *testing.T, ctx context.Context, events *codex.EventSubscription, id string) {
	t.Helper()
	for {
		select {
		case event, ok := <-events.Events():
			require.True(t, ok, "文件工具执行时事件流不能提前结束")
			if event.Method != "turn/completed" {
				continue
			}
			var params struct {
				Turn struct {
					ID, Status string
					Error      any
				}
			}
			require.NoError(t, json.Unmarshal(event.Params, &params))
			if params.Turn.ID != id {
				continue
			}
			require.Nil(t, params.Turn.Error)
			require.Equal(t, "completed", params.Turn.Status)
			return
		case <-ctx.Done():
			t.Fatal("真实文件工具未完成")
		}
	}
}
