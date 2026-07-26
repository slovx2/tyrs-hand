package worker

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/config"
	"go.uber.org/zap"
)

const browserAgentSocketName = "browser-agent.sock"

type browserAgentRelayManager struct {
	cfg         config.Config
	secret      string
	logger      *zap.Logger
	mu          sync.Mutex
	listeners   map[uuid.UUID]net.Listener
	connections map[uuid.UUID]map[net.Conn]struct{}
}

func newBrowserAgentRelayManager(cfg config.Config, logger *zap.Logger) (*browserAgentRelayManager, error) {
	secret, err := os.ReadFile(cfg.BrowserMCPTokenFile)
	if err != nil {
		return nil, err
	}
	if _, err := deriveBrowserToken(string(secret), "worker"); err != nil {
		return nil, err
	}
	return &browserAgentRelayManager{cfg: cfg, secret: string(secret), logger: logger,
		listeners:   make(map[uuid.UUID]net.Listener),
		connections: make(map[uuid.UUID]map[net.Conn]struct{})}, nil
}

func (m *browserAgentRelayManager) Ensure(environmentID uuid.UUID) error {
	if environmentID == uuid.Nil {
		return errors.New("浏览器 Agent 环境 ID 为空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.listeners[environmentID]; exists {
		return nil
	}
	directory := filepath.Join(m.cfg.DevelopmentRuntimeDir, environmentID.String())
	if err := os.MkdirAll(directory, 0o770); err != nil {
		return err
	}
	socketPath := filepath.Join(directory, browserAgentSocketName)
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(socketPath, 0o666); err != nil {
		_ = listener.Close()
		return err
	}
	m.listeners[environmentID] = listener
	go m.serve(environmentID, listener)
	return nil
}

func (m *browserAgentRelayManager) Reset(environmentID uuid.UUID) {
	m.mu.Lock()
	listener := m.listeners[environmentID]
	connections := m.connections[environmentID]
	delete(m.listeners, environmentID)
	delete(m.connections, environmentID)
	m.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	for connection := range connections {
		_ = connection.Close()
	}
	_ = os.Remove(filepath.Join(m.cfg.DevelopmentRuntimeDir, environmentID.String(),
		browserAgentSocketName))
}

func (m *browserAgentRelayManager) Close() {
	m.mu.Lock()
	listeners := m.listeners
	connections := m.connections
	m.listeners = make(map[uuid.UUID]net.Listener)
	m.connections = make(map[uuid.UUID]map[net.Conn]struct{})
	m.mu.Unlock()
	for environmentID, listener := range listeners {
		_ = listener.Close()
		_ = os.Remove(filepath.Join(m.cfg.DevelopmentRuntimeDir, environmentID.String(),
			browserAgentSocketName))
	}
	for _, environmentConnections := range connections {
		for connection := range environmentConnections {
			_ = connection.Close()
		}
	}
}

func (m *browserAgentRelayManager) serve(environmentID uuid.UUID, listener net.Listener) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go m.forward(environmentID, connection)
	}
}

func (m *browserAgentRelayManager) forward(environmentID uuid.UUID, agent net.Conn) {
	if !m.trackConnection(environmentID, agent) {
		_ = agent.Close()
		return
	}
	defer m.untrackConnection(environmentID, agent)
	defer func() { _ = agent.Close() }()
	connectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	upstream, err := (&net.Dialer{}).DialContext(connectCtx, "tcp",
		m.cfg.BrowserAgentRelayAddress)
	if err != nil {
		m.logger.Warn("连接 BrowserRegistry 失败", zap.Error(err))
		return
	}
	defer func() { _ = upstream.Close() }()
	token, err := deriveBrowserToken(m.secret, environmentID.String())
	if err != nil {
		return
	}
	if err := writeBrowserAgentFrame(upstream, map[string]string{
		"type": "register", "token": token,
	}); err != nil {
		return
	}
	finished := make(chan struct{}, 2)
	copyStream := func(destination, source net.Conn) {
		_, _ = io.Copy(destination, source)
		if unix, ok := destination.(*net.UnixConn); ok {
			_ = unix.CloseWrite()
		} else if tcp, ok := destination.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		finished <- struct{}{}
	}
	go copyStream(upstream, agent)
	go copyStream(agent, upstream)
	<-finished
}

func (m *browserAgentRelayManager) trackConnection(environmentID uuid.UUID,
	connection net.Conn,
) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, active := m.listeners[environmentID]; !active {
		return false
	}
	if m.connections[environmentID] == nil {
		m.connections[environmentID] = make(map[net.Conn]struct{})
	}
	m.connections[environmentID][connection] = struct{}{}
	return true
}

func (m *browserAgentRelayManager) untrackConnection(environmentID uuid.UUID,
	connection net.Conn,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.connections[environmentID], connection)
	if len(m.connections[environmentID]) == 0 {
		delete(m.connections, environmentID)
	}
}

func writeBrowserAgentFrame(writer io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(payload) > 64*1024*1024 {
		return errors.New("浏览器 Agent 帧过大")
	}
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(payload)))
	if err := writeBrowserAgentBytes(writer, header); err != nil {
		return err
	}
	return writeBrowserAgentBytes(writer, payload)
}

func writeBrowserAgentBytes(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
