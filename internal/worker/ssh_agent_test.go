package worker

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func TestSSHAgentGenerationLoadsKeysAndWritesPublicConfig(t *testing.T) {
	if _, err := exec.LookPath("ssh-agent"); err != nil {
		t.Skip("系统没有 ssh-agent")
	}
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(privateKey, "test")
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(privateKey)
	require.NoError(t, err)
	credentialID := uuid.New()
	configuration := workerprotocol.SSHConfiguration{
		Revision: "revision",
		Credentials: []workerprotocol.SSHCredential{{
			ID: credentialID, PrivateKey: string(pem.EncodeToMemory(block)),
			PublicKey:   string(ssh.MarshalAuthorizedKey(signer.PublicKey())),
			Fingerprint: ssh.FingerprintSHA256(signer.PublicKey()),
		}},
		Hosts: []workerprotocol.SSHHost{
			{Alias: "jump", Hostname: "192.0.2.1", Port: 22,
				Username: "ubuntu", CredentialID: credentialID},
			{Alias: "example", Hostname: "192.0.2.2", Port: 2222,
				Username: "deploy", CredentialID: credentialID, ProxyJumpAlias: "jump"},
		},
	}
	root, err := os.MkdirTemp("/tmp", "tyrs-ssh-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	manager := newSSHAgentManager(root, nil, zap.NewNop())
	require.NoError(t, os.MkdirAll(filepath.Join(manager.root, "keys"), 0o755))
	managed, err := manager.startGeneration(context.Background(), configuration)
	require.NoError(t, err)
	t.Cleanup(func() { _ = stopAgent(managed) })
	require.NotEqual(t, managed.socket, managed.agentSocket)
	socketInfo, err := os.Stat(managed.socket)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o666), socketInfo.Mode().Perm())

	connection, err := net.Dial("unix", managed.socket)
	require.NoError(t, err)
	defer func() { _ = connection.Close() }()
	keys, err := agent.NewClient(connection).List()
	require.NoError(t, err)
	require.Len(t, keys, 1)

	sshConfig, err := os.ReadFile(filepath.Join(manager.root, "ssh_config"))
	require.NoError(t, err)
	require.Contains(t, string(sshConfig), "Host example")
	require.Contains(t, string(sshConfig), "StrictHostKeyChecking accept-new")
	require.Contains(t, string(sshConfig), "IdentitiesOnly yes")
	require.Contains(t, string(sshConfig), "ProxyJump jump")
	require.Contains(t, string(sshConfig), "Port 2222")
	require.NotContains(t, string(sshConfig), "PRIVATE KEY")
	publicFile, err := os.ReadFile(filepath.Join(manager.root, "keys", credentialID.String()+".pub"))
	require.NoError(t, err)
	require.Contains(t, string(publicFile), "ssh-rsa")
}

func TestSSHAgentProxyStopsConnectionsAndRemovesSockets(t *testing.T) {
	if _, err := exec.LookPath("ssh-agent"); err != nil {
		t.Skip("系统没有 ssh-agent")
	}
	root, err := os.MkdirTemp("/tmp", "tyrs-ssh-proxy-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	require.NoError(t, os.MkdirAll(filepath.Join(root, "keys"), 0o755))
	manager := newSSHAgentManager(root, nil, zap.NewNop())
	managed, err := manager.startGeneration(context.Background(),
		workerprotocol.SSHConfiguration{Revision: "empty"})
	require.NoError(t, err)

	connection, err := net.Dial("unix", managed.socket)
	require.NoError(t, err)
	_, err = agent.NewClient(connection).List()
	require.NoError(t, err)
	require.NoError(t, stopAgent(managed))
	require.NoError(t, connection.Close())
	require.NoFileExists(t, managed.socket)
	require.NoFileExists(t, managed.agentSocket)
}

func TestSSHAgentGenerationSupportsEncryptedKeysAndRejectsInvalidGeneration(t *testing.T) {
	if _, err := exec.LookPath("ssh-agent"); err != nil {
		t.Skip("系统没有 ssh-agent")
	}
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(privateKey, "encrypted", []byte("secret"))
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(privateKey)
	require.NoError(t, err)
	root, err := os.MkdirTemp("/tmp", "tyrs-ssh-encrypted-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	manager := newSSHAgentManager(root, nil, zap.NewNop())
	require.NoError(t, os.MkdirAll(filepath.Join(root, "keys"), 0o755))

	credentialID := uuid.New()
	managed, err := manager.startGeneration(context.Background(), workerprotocol.SSHConfiguration{
		Revision: "encrypted",
		Credentials: []workerprotocol.SSHCredential{{
			ID: credentialID, PrivateKey: string(pem.EncodeToMemory(block)), Passphrase: "secret",
			PublicKey: string(ssh.MarshalAuthorizedKey(signer.PublicKey())),
		}},
	})
	require.NoError(t, err)
	require.NoError(t, stopAgent(managed))

	_, err = manager.startGeneration(context.Background(), workerprotocol.SSHConfiguration{
		Revision: "invalid", Credentials: []workerprotocol.SSHCredential{{
			ID: uuid.New(), PrivateKey: "not-a-key",
		}},
	})
	require.ErrorContains(t, err, "解析 SSH 凭证")
}

func TestSSHAgentStatusAndSocketSwitch(t *testing.T) {
	root := t.TempDir()
	targetA := filepath.Join(root, "agent-a.sock")
	targetB := filepath.Join(root, "agent-b.sock")
	link := filepath.Join(root, "current.sock")
	require.NoError(t, switchSymlink(link, targetA))
	destination, err := os.Readlink(link)
	require.NoError(t, err)
	require.Equal(t, filepath.Base(targetA), destination)
	require.NoError(t, switchSymlink(link, targetB))
	destination, err = os.Readlink(link)
	require.NoError(t, err)
	require.Equal(t, filepath.Base(targetB), destination)

	manager := newSSHAgentManager(root, nil, zap.NewNop())
	manager.setError(context.DeadlineExceeded)
	require.Equal(t, "error", manager.Status().Status)
	manager.current = &managedAgent{}
	manager.setError(context.DeadlineExceeded)
	status := manager.Status()
	require.Equal(t, "degraded", status.Status)
	require.Contains(t, status.LastError, "deadline")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = waitForSocket(ctx, filepath.Join(root, "missing.sock"))
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestSSHAgentSyncKeepsCurrentGenerationWhenRotationIsInvalid(t *testing.T) {
	if _, err := exec.LookPath("ssh-agent"); err != nil {
		t.Skip("系统没有 ssh-agent")
	}
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(privateKey, "sync")
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(privateKey)
	require.NoError(t, err)
	credentialID := uuid.New()
	configuration := workerprotocol.SSHConfiguration{
		Revision: "v1",
		Credentials: []workerprotocol.SSHCredential{{
			ID: credentialID, PrivateKey: string(pem.EncodeToMemory(block)),
			PublicKey: string(ssh.MarshalAuthorizedKey(signer.PublicKey())),
		}},
		Hosts: []workerprotocol.SSHHost{{Alias: "server", Hostname: "192.0.2.10",
			Port: 22, Username: "ubuntu", CredentialID: credentialID}},
	}
	etag := `"v1"`
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		require.Equal(t, "Bearer node-token", request.Header.Get("Authorization"))
		if request.Header.Get("If-None-Match") == etag {
			response.WriteHeader(http.StatusNotModified)
			return
		}
		response.Header().Set("ETag", etag)
		_ = json.NewEncoder(response).Encode(configuration)
	}))
	t.Cleanup(server.Close)
	root, err := os.MkdirTemp("/tmp", "tyrs-ssh-sync-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	require.NoError(t, os.MkdirAll(filepath.Join(root, "keys"), 0o755))
	client := workerprotocol.NewClient(server.URL, "node-token", time.Second)
	manager := newSSHAgentManager(root, client, zap.NewNop())
	t.Cleanup(manager.Close)

	require.NoError(t, manager.sync(context.Background()))
	require.NotNil(t, manager.current)
	currentSocket := manager.current.socket
	require.Equal(t, etag, manager.etag)
	require.NoError(t, manager.sync(context.Background()), "304 不应重建 Agent")
	require.Equal(t, currentSocket, manager.current.socket)

	configuration.Revision = "v2"
	configuration.Credentials[0].PrivateKey = "invalid-rotation"
	etag = `"v2"`
	err = manager.sync(context.Background())
	require.ErrorContains(t, err, "解析 SSH 凭证")
	require.Equal(t, currentSocket, manager.current.socket)
	require.Equal(t, `"v1"`, manager.etag)
	destination, err := os.Readlink(filepath.Join(root, "current.sock"))
	require.NoError(t, err)
	require.Equal(t, filepath.Base(currentSocket), destination)
}
