package sshtransport

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestPinnedHostKeyRejectsChangedFingerprint(t *testing.T) {
	_, firstPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	first, err := ssh.NewPublicKey(firstPrivate.Public())
	require.NoError(t, err)
	_, secondPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	second, err := ssh.NewPublicKey(secondPrivate.Public())
	require.NoError(t, err)

	callback := pinnedHostKeyCallback(ssh.FingerprintSHA256(first))
	require.NoError(t, callback("host", nil, first))
	require.ErrorContains(t, callback("host", nil, second), "主机指纹已变化")
}

func TestConnectRequiresConfirmedHostFingerprint(t *testing.T) {
	_, err := connectSSH(t.Context(), sshOptions{host: "127.0.0.1", port: 22, user: "test"})
	require.EqualError(t, err, "尚未确认 SSH 主机 SHA-256 指纹")
}
