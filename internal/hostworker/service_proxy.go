package hostworker

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	serviceProxyMagic   = "TYSP"
	serviceProxyTimeout = 5 * time.Second
)

type serviceProxy struct {
	path      string
	listener  net.Listener
	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	closed    bool
	clients   map[net.Conn]struct{}
}

func startServiceProxy(socketPath string) (*serviceProxy, error) {
	if socketPath == "" {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o770); err != nil {
		return nil, err
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socketPath, 0o660); err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return nil, err
	}
	proxy := &serviceProxy{path: socketPath, listener: listener, done: make(chan struct{}),
		clients: make(map[net.Conn]struct{})}
	go proxy.serve()
	return proxy, nil
}

func (p *serviceProxy) serve() {
	defer close(p.done)
	for {
		connection, err := p.listener.Accept()
		if err != nil {
			return
		}
		if !p.register(connection) {
			continue
		}
		go func() {
			defer p.unregister(connection)
			handleServiceProxyConnection(connection)
		}()
	}
}

func (p *serviceProxy) close() {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		clients := make([]net.Conn, 0, len(p.clients))
		for connection := range p.clients {
			clients = append(clients, connection)
		}
		p.mu.Unlock()
		_ = p.listener.Close()
		<-p.done
		for _, connection := range clients {
			_ = connection.Close()
		}
		_ = os.Remove(p.path)
	})
}

func (p *serviceProxy) register(connection net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = connection.Close()
		return false
	}
	p.clients[connection] = struct{}{}
	return true
}

func (p *serviceProxy) unregister(connection net.Conn) {
	p.mu.Lock()
	delete(p.clients, connection)
	p.mu.Unlock()
}

func handleServiceProxyConnection(connection net.Conn) {
	defer func() { _ = connection.Close() }()
	_ = connection.SetReadDeadline(time.Now().Add(serviceProxyTimeout))
	header := make([]byte, len(serviceProxyMagic)+2)
	if _, err := io.ReadFull(connection, header); err != nil {
		return
	}
	if string(header[:len(serviceProxyMagic)]) != serviceProxyMagic {
		_ = writeServiceProxyStatus(connection, errors.New("服务代理协议无效"))
		return
	}
	port := int(binary.BigEndian.Uint16(header[len(serviceProxyMagic):]))
	if port < 1 {
		_ = writeServiceProxyStatus(connection, errors.New("服务端口无效"))
		return
	}
	upstream, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1",
		fmt.Sprintf("%d", port)), serviceProxyTimeout)
	if err != nil {
		_ = writeServiceProxyStatus(connection, fmt.Errorf("连接开发服务: %w", err))
		return
	}
	defer func() { _ = upstream.Close() }()
	if err := writeServiceProxyStatus(connection, nil); err != nil {
		return
	}
	_ = connection.SetDeadline(time.Time{})
	copyBidirectional(connection, upstream)
}

func writeServiceProxyStatus(connection net.Conn, statusErr error) error {
	message := ""
	code := byte(0)
	if statusErr != nil {
		code = 1
		message = statusErr.Error()
		if len(message) > 65535 {
			message = message[:65535]
		}
	}
	header := []byte{code, 0, 0}
	binary.BigEndian.PutUint16(header[1:], uint16(len(message)))
	if _, err := connection.Write(header); err != nil {
		return err
	}
	if message != "" {
		_, err := io.WriteString(connection, message)
		return err
	}
	return nil
}

func copyBidirectional(left, right net.Conn) {
	finished := make(chan struct{}, 2)
	copyOne := func(destination, source net.Conn) {
		_, _ = io.Copy(destination, source)
		closeWrite(destination)
		finished <- struct{}{}
	}
	go copyOne(left, right)
	go copyOne(right, left)
	<-finished
	<-finished
}

func closeWrite(connection net.Conn) {
	switch value := connection.(type) {
	case *net.TCPConn:
		_ = value.CloseWrite()
	case *net.UnixConn:
		_ = value.CloseWrite()
	}
}
