//go:build integration

package hostworker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// MCP-013：取消发生在真实授权码交换已到达之后，迟到令牌不能落盘。
func verifyRuntimeOAuthCancellation(t *testing.T, ctx context.Context, registry *RuntimeRegistry, signer ssh.Signer, fixture string) {
	t.Helper()
	entry := registry.entries[runtimeidentity.Claude]
	root := entry.Runtime.WorkspaceRoot()
	credentials := func() map[string][32]byte {
		files, err := filepath.Glob(filepath.Join(entry.Runtime.options.StateDir, "mcp-oauth", "*.json"))
		require.NoError(t, err)
		result := make(map[string][32]byte, len(files))
		for _, file := range files {
			value, err := os.ReadFile(file)
			require.NoError(t, err)
			result[filepath.Base(file)] = sha256.Sum256(value)
		}
		return result
	}
	baseline := credentials()
	require.Len(t, baseline, 1, "前置真实 OAuth 必须已有一份有效凭据")
	connect := func() *ssh.Client {
		connection, err := ssh.Dial("tcp", entry.SSH.Addr().String(), &ssh.ClientConfig{
			User: "test", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, Timeout: time.Second,
			HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
				if ssh.FingerprintSHA256(key) != entry.SSH.HostKeyFingerprint() {
					return fmt.Errorf("Host Key 不匹配")
				}
				return nil
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = connection.Close() })
		return connection
	}
	var evidence []map[string]any
	for _, reason := range []string{"channel", "owner", "connection", "hub", "server"} {
		effect := filepath.Join(root, "oauth-cancel-"+reason+".txt")
		code := fmt.Sprintf(`import { OAuthMcpFixture } from %q;
import { createInterface } from 'node:readline';
import { setImmediate } from 'node:timers/promises';
const fixture = new OAuthMcpFixture(%q);
const gate = fixture.pauseTokenExchange();
console.log(JSON.stringify({url: await fixture.start()}));
gate.started.then(() => console.log(JSON.stringify({stage:'started'})));
createInterface({input:process.stdin}).on('line', async () => {
  gate.release();
  while (fixture.exchanges === 0 && fixture.errors.length === 0) await setImmediate();
  console.log(JSON.stringify({stage:'released', exchanges:fixture.exchanges, errors:fixture.errors.length}));
});
process.on('SIGTERM', async () => {gate.release(); await fixture.close(); process.exit(0)});`, fixture, effect)
		command := exec.CommandContext(ctx, "node", "--input-type=module", "-e", code)
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root}
		output, err := command.StdoutPipe()
		require.NoError(t, err)
		input, err := command.StdinPipe()
		require.NoError(t, err)
		require.NoError(t, command.Start())
		t.Cleanup(func() { _ = command.Process.Signal(syscall.SIGTERM); _ = command.Wait() })
		decoder := json.NewDecoder(output)
		var message struct {
			URL, Stage        string
			Exchanges, Errors int
		}
		require.NoError(t, decoder.Decode(&message))
		connection := connect()
		owner, trace := connectRuntimeSSHWithTrace(t, ctx, connection, runtimeidentity.Claude, codex.SocketClientOptions{RequestTimeout: 10 * time.Second})
		name := "cancel-" + reason
		require.NoError(t, owner.Call(ctx, "config/value/write", map[string]any{"keyPath": "mcp_servers", "mergeStrategy": "replace", "value": map[string]any{name: map[string]any{"url": message.URL, "http_headers": map[string]string{"X-Resource-Only": "resource-test-secret"}}}}, nil))
		var login struct{ AuthorizationURL string }
		require.NoError(t, owner.Call(ctx, "mcpServer/oauth/login", map[string]any{"name": name, "timeoutSecs": 30}, &login))
		authorize := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := authorize.Get(login.AuthorizationURL)
		require.NoError(t, err)
		require.Equal(t, http.StatusFound, response.StatusCode)
		callback, err := url.Parse(response.Header.Get("Location"))
		_ = response.Body.Close()
		require.NoError(t, err)
		tunnel, err := connection.Dial("tcp", callback.Host)
		require.NoError(t, err)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, callback.String(), nil)
		require.NoError(t, err)
		require.NoError(t, request.Write(tunnel))
		require.NoError(t, decoder.Decode(&message))
		require.Equal(t, "started", message.Stage)
		ended := make(chan struct{})
		go func() { _, _ = io.Copy(io.Discard, tunnel); _ = tunnel.Close(); close(ended) }()
		closed := make(chan error, 1)
		trace.expectClose("client-disconnect")
		go func() {
			switch reason {
			case "channel":
				closed <- tunnel.Close()
			case "owner":
				closed <- owner.Close()
			case "connection":
				closed <- connection.Close()
			case "hub":
				closed <- registry.Restart(runtimeidentity.Claude)
			case "server":
				closed <- entry.SSH.Close()
			}
		}()
		select {
		case err := <-closed:
			require.NoError(t, err)
		case <-time.After(3 * time.Second):
			t.Fatalf("%s 关闭被 OAuth 在途请求阻塞", reason)
		}
		select {
		case <-ended:
		case <-time.After(time.Second):
			t.Fatalf("%s 未终止真实 SSH 回调", reason)
		}
		_, err = io.WriteString(input, "release\n")
		require.NoError(t, err)
		require.NoError(t, decoder.Decode(&message))
		require.Equal(t, "released", message.Stage)
		require.Equal(t, 1, message.Exchanges, "迟到 token 响应必须真实产生")
		require.Zero(t, message.Errors)
		if reason == "server" {
			entry.SSH, err = StartSSHServer(registry.ctx, entry.SSH.options)
			require.NoError(t, err)
		}
		observer := connectRuntimeSSH(t, ctx, connect(), runtimeidentity.Claude)
		var status struct {
			Data []struct{ Name, AuthStatus string }
		}
		require.NoError(t, observer.Call(ctx, "mcpServerStatus/list", map[string]any{}, &status))
		require.Len(t, status.Data, 1)
		require.Equal(t, name, status.Data[0].Name)
		require.Equal(t, "notLoggedIn", status.Data[0].AuthStatus, "取消后不能接受迟到令牌")
		require.Equal(t, baseline, credentials(), "取消不能保存新凭据或损坏旧凭据")
		_, err = os.Stat(effect)
		require.True(t, os.IsNotExist(err), "取消不得触发 MCP 文件副作用")
		evidence = append(evidence, map[string]any{"reason": reason, "realTokenExchangeStarted": true, "lateTokenProduced": true, "persistedNewCredentials": false, "authStatus": "notLoggedIn", "sideEffects": 0})
	}
	if directory := os.Getenv("PROTOCOL_ARTIFACT_DIR"); directory != "" {
		data, err := json.MarshalIndent(evidence, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(directory, "oauth-ssh-cancellation.json"), data, 0o600))
	}
}
