package worker

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestBrowserAgentRelayScopesConnectionToEnvironment(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = upstream.Close() }()
	root, err := os.MkdirTemp("/tmp", "tyrs-browser-relay-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	secretPath := filepath.Join(root, "browser-secret")
	require.NoError(t, os.WriteFile(secretPath, []byte("secret\n"), 0o600))
	manager, err := newBrowserAgentRelayManager(config.Config{
		BrowserMCPTokenFile: secretPath, DevelopmentRuntimeDir: root,
		BrowserAgentRelayAddress: upstream.Addr().String(),
	}, zap.NewNop())
	require.NoError(t, err)
	defer manager.Close()
	environmentID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	require.NoError(t, manager.Ensure(environmentID))

	registration := make(chan map[string]string, 1)
	go func() {
		connection, acceptErr := upstream.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		header := make([]byte, 4)
		_, _ = io.ReadFull(connection, header)
		payload := make([]byte, binary.BigEndian.Uint32(header))
		_, _ = io.ReadFull(connection, payload)
		var value map[string]string
		_ = json.Unmarshal(payload, &value)
		registration <- value
		_, _ = connection.Write([]byte("TYRS-BROWSER/1\n"))
	}()

	agent, err := net.DialTimeout("unix", filepath.Join(root, environmentID.String(),
		browserAgentSocketName), time.Second)
	require.NoError(t, err)
	defer func() { _ = agent.Close() }()
	preface := make([]byte, len("TYRS-BROWSER/1\n"))
	_, err = io.ReadFull(agent, preface)
	require.NoError(t, err)
	require.Equal(t, "TYRS-BROWSER/1\n", string(preface))
	value := <-registration
	expected, err := deriveBrowserToken("secret", environmentID.String())
	require.NoError(t, err)
	require.Equal(t, map[string]string{"type": "register", "token": expected}, value)
}

func TestBrowserAgentRelayResetClosesActiveConnection(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = upstream.Close() }()
	root, err := os.MkdirTemp("/tmp", "tyrs-browser-reset-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	secretPath := filepath.Join(root, "browser-secret")
	require.NoError(t, os.WriteFile(secretPath, []byte("secret\n"), 0o600))
	manager, err := newBrowserAgentRelayManager(config.Config{
		BrowserMCPTokenFile: secretPath, DevelopmentRuntimeDir: root,
		BrowserAgentRelayAddress: upstream.Addr().String(),
	}, zap.NewNop())
	require.NoError(t, err)
	defer manager.Close()
	environmentID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	require.NoError(t, manager.Ensure(environmentID))

	registered := make(chan struct{})
	go func() {
		connection, acceptErr := upstream.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		header := make([]byte, 4)
		if _, readErr := io.ReadFull(connection, header); readErr != nil {
			return
		}
		payload := make([]byte, binary.BigEndian.Uint32(header))
		if _, readErr := io.ReadFull(connection, payload); readErr != nil {
			return
		}
		close(registered)
		_, _ = io.Copy(io.Discard, connection)
	}()

	agent, err := net.DialTimeout("unix", filepath.Join(root, environmentID.String(),
		browserAgentSocketName), time.Second)
	require.NoError(t, err)
	defer func() { _ = agent.Close() }()
	select {
	case <-registered:
	case <-time.After(time.Second):
		t.Fatal("Browser Agent relay 未注册到上游")
	}
	manager.Reset(environmentID)
	require.NoError(t, agent.SetReadDeadline(time.Now().Add(time.Second)))
	buffer := make([]byte, 1)
	_, err = agent.Read(buffer)
	require.Error(t, err)
	require.False(t, managerHasAgentConnections(manager, environmentID))
}

func managerHasAgentConnections(manager *browserAgentRelayManager, environmentID uuid.UUID) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return len(manager.connections[environmentID]) > 0
}
