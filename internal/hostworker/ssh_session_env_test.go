package hostworker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

// Claude 入口的会话环境不继承宿主变量；Codex Desktop 远程启动器执行探测等非 proxy
// 负载前要求 SHELL 指向可执行的登录 shell。
func TestSSHServerClaudeSessionExportsLoginShellForDesktopLauncher(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(private)
	require.NoError(t, err)
	publicKey, err := ssh.NewPublicKey(public)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runtime := &Runtime{options: RuntimeOptions{Engine: runtimeidentity.Claude,
		StateDir: t.TempDir(), CodexHome: t.TempDir(), Logger: zap.NewNop()}}
	server, err := StartSSHServer(ctx, SSHOptions{
		ListenAddr: "127.0.0.1:0", HostKeyFile: filepath.Join(t.TempDir(), "host_key"),
		Home: t.TempDir(), CodexHome: runtime.CodexHome(), Shell: "/bin/sh",
		AuthorizedClients: []AuthorizedClient{{ID: "desktop", PublicKey: publicKey}},
		Runtime:           runtime,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	client, err := ssh.Dial("tcp", server.Addr().String(), &ssh.ClientConfig{
		User: "ignored", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	launcher := `sh -c 'if [ -z "$SHELL" ] || [ ! -x "$SHELL" ]; then ` +
		`echo "Codex remote SSH requires SHELL to point to an executable login shell" >&2; exit 127; fi; ` +
		`CODEX_REMOTE_PAYLOAD="$1"; export CODEX_REMOTE_PAYLOAD; ` +
		`exec "$SHELL" -l -c '\''exec /bin/sh -c "$CODEX_REMOTE_PAYLOAD"'\''' sh 'printf "%s" "$SHELL"'`
	session, err := client.NewSession()
	require.NoError(t, err)
	output, err := session.CombinedOutput(launcher)
	require.NoError(t, err, string(output))
	require.Equal(t, "/bin/sh", string(output))
}
