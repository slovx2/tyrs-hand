package codexrelay

import (
	"errors"
	"net"
	"net/http"
	"sync"
)

// ServeConn 在单条已认证传输上提供 Codex Desktop WebSocket 协议。
// 传输的认证和生命周期由宿主 Worker 管理，Relay 不再需要公开监听地址。
func (r *Relay) ServeConn(connection net.Conn) error {
	if connection == nil {
		return errors.New("desktop Codex 连接不能为空")
	}
	done := make(chan struct{})
	tracked := &signalConnection{Conn: connection, done: done}
	listener := &singleConnectionListener{connection: tracked, done: done}
	server := &http.Server{Handler: http.HandlerFunc(r.serveDesktop)}
	err := server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

type singleConnectionListener struct {
	connection net.Conn
	done       chan struct{}
	once       sync.Once
}

type signalConnection struct {
	net.Conn
	done chan struct{}
	once sync.Once
}

func (c *signalConnection) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}

func (l *singleConnectionListener) Accept() (net.Conn, error) {
	var connection net.Conn
	l.once.Do(func() { connection = l.connection })
	if connection != nil {
		return connection, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *singleConnectionListener) Close() error {
	return l.connection.Close()
}

func (l *singleConnectionListener) Addr() net.Addr { return l.connection.LocalAddr() }
