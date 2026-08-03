package hostworker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

type desktopStub struct{}

func (desktopStub) ServeDesktop(connection net.Conn) error {
	_, err := connection.Write([]byte("desktop-proxy"))
	return err
}

func TestSSHServerSupportsShellProxyAndRejectsForwarding(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(private)
	require.NoError(t, err)
	publicKey, err := ssh.NewPublicKey(public)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	server, err := StartSSHServer(ctx, SSHOptions{
		ListenAddr: "127.0.0.1:0", HostKeyFile: filepath.Join(t.TempDir(), "host_key"),
		Home: t.TempDir(), CodexHome: t.TempDir(), Shell: "/bin/sh",
		AuthorizedClients: []AuthorizedClient{{ID: "desktop", PublicKey: publicKey}},
		Runtime:           desktopStub{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	client, err := ssh.Dial("tcp", server.Addr().String(), &ssh.ClientConfig{
		User: "ignored", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	session, err := client.NewSession()
	require.NoError(t, err)
	output, err := session.Output("printf host-shell")
	require.NoError(t, err)
	require.Equal(t, "host-shell", string(output))

	session, err = client.NewSession()
	require.NoError(t, err)
	output, err = session.Output("codex app-server proxy")
	require.NoError(t, err)
	require.Equal(t, "desktop-proxy", string(output))

	_, err = client.Dial("tcp", "127.0.0.1:80")
	require.Error(t, err)
}
