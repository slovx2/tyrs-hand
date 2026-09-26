package hostworker

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// 此处只测试真实 SSH 的取消信号；SDK 与授权副作用由 MCP-013 全链用例验证。
type oauthCancellationRuntime struct {
	desktopStub
	arrived  chan struct{}
	canceled chan struct{}
}

func (*oauthCancellationRuntime) OAuthCallbackAllowed(host string, port uint32) bool {
	return host == "127.0.0.1" && port == 12345
}

func (r *oauthCancellationRuntime) ServeOAuthCallback(ctx context.Context, _ string, _ uint32, stream io.ReadWriteCloser) error {
	request, err := http.ReadRequest(bufio.NewReader(stream))
	if err != nil {
		return err
	}
	_ = request.Body.Close()
	close(r.arrived)
	<-ctx.Done()
	close(r.canceled)
	return ctx.Err()
}

func TestSSHOAuthForwardCancellation(t *testing.T) {
	for _, reason := range []string{"channel", "connection", "server"} {
		t.Run(reason, func(t *testing.T) {
			public, private, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			signer, err := ssh.NewSignerFromKey(private)
			require.NoError(t, err)
			publicKey, err := ssh.NewPublicKey(public)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runtime := &oauthCancellationRuntime{arrived: make(chan struct{}), canceled: make(chan struct{})}
			server, err := StartSSHServer(ctx, SSHOptions{
				ListenAddr: "127.0.0.1:0", HostKeyFile: filepath.Join(t.TempDir(), "host-key"),
				Home: t.TempDir(), CodexHome: t.TempDir(), Runtime: runtime,
				AuthorizedClients: []AuthorizedClient{{ID: "test-client", PublicKey: publicKey}},
			})
			require.NoError(t, err)
			defer func() { _ = server.Close() }()
			client, err := ssh.Dial("tcp", server.Addr().String(), &ssh.ClientConfig{User: "test", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.InsecureIgnoreHostKey()})
			require.NoError(t, err)
			defer func() { _ = client.Close() }()
			destination := struct {
				Host       string
				Port       uint32
				OriginHost string
				OriginPort uint32
			}{"127.0.0.1", 12345, "127.0.0.1", 1}
			channel, requests, err := client.OpenChannel("direct-tcpip", ssh.Marshal(destination))
			require.NoError(t, err)
			go ssh.DiscardRequests(requests)
			_, err = io.WriteString(channel, "GET /callback HTTP/1.1\r\nHost: localhost\r\n\r\n")
			require.NoError(t, err)
			select {
			case <-runtime.arrived:
			case <-time.After(time.Second):
				t.Fatal("真实 SSH 回调未到达")
			}
			require.NoError(t, channel.CloseWrite())
			select {
			case <-runtime.canceled:
				t.Fatal("写侧 EOF 不应取消等待响应的 HTTP 请求")
			case <-time.After(20 * time.Millisecond):
			}
			finished := make(chan struct{})
			go func() {
				switch reason {
				case "channel":
					_ = channel.Close()
				case "connection":
					_ = client.Close()
				case "server":
					_ = server.Close()
				}
				close(finished)
			}()
			select {
			case <-runtime.canceled:
			case <-time.After(time.Second):
				t.Fatal("SSH 关闭未取消在途 OAuth 请求")
			}
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("SSH 关闭仍等待 OAuth 超时")
			}
		})
	}
}
