package hostworker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestAuthorizationWatchRevokesUnreadableAndMalformedFile(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key, err := ssh.NewPublicKey(public)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "authorized_keys")
	ctx, cancel := context.WithCancel(t.Context())
	registry := &RuntimeRegistry{ctx: ctx, cancel: cancel,
		Authorization: NewClientAuthorization([]AuthorizedClient{{ID: "test", PublicKey: key}})}
	t.Cleanup(func() { require.NoError(t, registry.Close()) })
	registry.WatchAuthorizedClients(path, nil)
	allowed := func() bool { _, ok := registry.Authorization.lookup(string(key.Marshal())); return ok }
	require.Eventually(t, func() bool { return !allowed() }, 3*time.Second, 10*time.Millisecond, "丢失授权文件必须撤销")
	require.NoError(t, os.WriteFile(path, ssh.MarshalAuthorizedKey(key), 0o600))
	require.Eventually(t, allowed, 3*time.Second, 10*time.Millisecond, "修复后自动恢复")
	require.NoError(t, os.WriteFile(path, []byte("malformed key"), 0o600))
	require.Eventually(t, func() bool { return !allowed() }, 3*time.Second, 10*time.Millisecond, "格式损坏不能保留旧授权")
	require.NoError(t, os.WriteFile(path, ssh.MarshalAuthorizedKey(key), 0o600))
	require.Eventually(t, allowed, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	require.Eventually(t, func() bool { return !allowed() }, 3*time.Second, 10*time.Millisecond, "空文件表示撤销所有客户端")
}
