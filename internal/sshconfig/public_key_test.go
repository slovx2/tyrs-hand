package sshconfig

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestParseAuthorizedPublicKey(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key, err := ssh.NewPublicKey(public)
	require.NoError(t, err)
	input := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " laptop"

	normalized, fingerprint, err := ParseAuthorizedPublicKey(input)
	require.NoError(t, err)
	require.Equal(t, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), normalized)
	require.Equal(t, ssh.FingerprintSHA256(key), fingerprint)
}

func TestParseAuthorizedPublicKeyRejectsOptionsAndMultipleKeys(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key, err := ssh.NewPublicKey(public)
	require.NoError(t, err)
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))

	_, _, err = ParseAuthorizedPublicKey(`command="false" ` + line)
	require.ErrorContains(t, err, "options")
	_, _, err = ParseAuthorizedPublicKey(line + "\n" + line)
	require.ErrorContains(t, err, "一个")
}
