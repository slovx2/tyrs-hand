package hostworker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

// Codex Desktop 远程启动器执行探测等非 proxy 负载前要求 SHELL 指向可执行的登录 shell；
// 启动器未被识别时，登录 shell 即使重置 PATH，负载中的 codex 仍须解析为本入口包装器。
// 会话继承宿主环境，但不含 Worker 密钥与宿主模型凭据。
func TestSSHServerClaudeSessionSupportsUnrecognizedDesktopLauncher(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(private)
	require.NoError(t, err)
	publicKey, err := ssh.NewPublicKey(public)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runtime := &Runtime{options: RuntimeOptions{Engine: runtimeidentity.Claude,
		StateDir: t.TempDir(), CodexHome: t.TempDir(), Logger: zap.NewNop(),
		Environment: []string{"PATH=" + os.Getenv("PATH"), "USER=alice",
			"TYRS_HAND_WORKER_CREDENTIAL=worker-secret", "ANTHROPIC_API_KEY=host-model-secret"}}}
	require.NoError(t, os.MkdirAll(runtime.EntryBin(), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(runtime.EntryBin(), "codex"),
		[]byte("#!/bin/sh\nprintf entry-codex\n"), 0o700))
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
		`exec "$SHELL" -l -c '\''PATH=/usr/bin:/bin; exec /bin/sh -c "$CODEX_REMOTE_PAYLOAD"'\''' sh ` +
		`'PATH="${CODEX_INSTALL_DIR:-$HOME/.local/bin}:$PATH"; export PATH; codex; ` +
		`printf "|%s|%s|%s" "$SHELL" "$USER" "${TYRS_HAND_WORKER_CREDENTIAL:-}${ANTHROPIC_API_KEY:-}"'`
	session, err := client.NewSession()
	require.NoError(t, err)
	output, err := session.CombinedOutput(launcher)
	require.NoError(t, err, string(output))
	require.Equal(t, "entry-codex|/bin/sh|alice|", string(output))
}
