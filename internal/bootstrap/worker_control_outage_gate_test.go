//go:build integration

package bootstrap

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"
)

// 仅切断真实 TCP 字节流，不生成 HTTP 状态或业务响应。
type controlTransportGate struct {
	listener    net.Listener
	upstream    string
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	blocked     bool
	closed      bool
	epoch       uint64
	connections map[net.Conn]net.Conn
	workers     sync.WaitGroup
	acceptDone  chan struct{}
}

func newControlTransportGate(t *testing.T, upstreamURL string) *controlTransportGate {
	t.Helper()
	u, err := url.Parse(upstreamURL)
	if err != nil || u.Scheme != "http" || u.Host == "" || (u.Path != "" && u.Path != "/") {
		t.Fatalf("网络闸门只接受测试 fixture 的 HTTP 根地址: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	gate := &controlTransportGate{listener: listener, upstream: u.Host,
		ctx: ctx, cancel: cancel, connections: make(map[net.Conn]net.Conn),
		acceptDone: make(chan struct{})}
	go gate.accept()
	t.Cleanup(gate.Close)
	return gate
}

func (g *controlTransportGate) URL() string {
	return "http://" + g.listener.Addr().String()
}

func (g *controlTransportGate) accept() {
	defer close(g.acceptDone)
	for {
		client, err := g.listener.Accept()
		if err != nil {
			return
		}
		g.mu.Lock()
		if g.blocked || g.closed {
			g.mu.Unlock()
			_ = client.Close()
			continue
		}
		epoch := g.epoch
		g.connections[client] = nil
		g.workers.Add(1)
		g.mu.Unlock()
		go g.forward(client, epoch)
	}
}

func (g *controlTransportGate) forward(client net.Conn, epoch uint64) {
	defer g.workers.Done()
	defer func() {
		g.mu.Lock()
		delete(g.connections, client)
		g.mu.Unlock()
		_ = client.Close()
	}()
	dialer := net.Dialer{Timeout: time.Second}
	upstream, err := dialer.DialContext(g.ctx, "tcp", g.upstream)
	if err != nil {
		return
	}
	defer func() { _ = upstream.Close() }()
	g.mu.Lock()
	_, tracked := g.connections[client]
	// 阻止离线前发起、重新上线后才完成的 Dial 混入新一代连接。
	if g.closed || g.blocked || !tracked || epoch != g.epoch {
		g.mu.Unlock()
		return
	}
	g.connections[client] = upstream
	g.mu.Unlock()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
	_ = client.Close()
	_ = upstream.Close()
	<-done
}

// 返回后现有连接均已 Close；in-flight 请求可能在关门前到达真实 Control，
// 测试必须以业务屏障确认注入窗口，不能把 Close 当作撤回已提交事务。
func (g *controlTransportGate) Offline() {
	g.mu.Lock()
	g.blocked = true
	g.epoch++
	connections := make([]net.Conn, 0, 2*len(g.connections))
	for client, upstream := range g.connections {
		connections = append(connections, client)
		if upstream != nil {
			connections = append(connections, upstream)
		}
	}
	g.mu.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
}

func (g *controlTransportGate) Online() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return fmt.Errorf("网络闸门已关闭")
	}
	g.blocked = false
	return nil
}

func (g *controlTransportGate) Close() {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.closed = true
	g.mu.Unlock()
	g.Offline()
	g.cancel()
	_ = g.listener.Close()
	<-g.acceptDone
	g.workers.Wait()
}
