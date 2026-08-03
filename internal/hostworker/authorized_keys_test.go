package hostworker

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestParseAuthorizedClientsSupportsMultipleKeys(t *testing.T) {
	lines := make([]string, 0, 2)
	for _, name := range []string{"desktop", "laptop"} {
		public, _, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		key, err := ssh.NewPublicKey(public)
		require.NoError(t, err)
		lines = append(lines, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))+" "+name)
	}
	clients, err := ParseAuthorizedClients([]byte(strings.Join(lines, "\n")))
	require.NoError(t, err)
	require.Len(t, clients, 2)
	require.Equal(t, "desktop", clients[0].ID)
	require.Equal(t, "laptop", clients[1].ID)
}

func TestParseAuthorizedClientsRejectsOptionsAndDuplicates(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key, err := ssh.NewPublicKey(public)
	require.NoError(t, err)
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	_, err = ParseAuthorizedClients([]byte(`command="false" ` + line))
	require.Error(t, err)
	_, err = ParseAuthorizedClients([]byte(line + "\n" + line))
	require.Error(t, err)
}
