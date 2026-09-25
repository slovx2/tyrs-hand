package hostworker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// 入口包装器只连接 Worker 管理的 Hub，不启动或寻找宿主 Codex。
type entryGateway struct {
	runtime     *Runtime
	listener    net.Listener
	mu          sync.Mutex
	connections map[net.Conn]struct{}
	closed      bool
	wg          sync.WaitGroup
}

type entryReply struct {
	Data  string `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
	Proxy bool   `json:"proxy,omitempty"`
}

func startEntryGateway(runtime *Runtime) (*entryGateway, error) {
	socket := filepath.Join(runtime.StateDir(), "entry.sock")
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	gateway := &entryGateway{runtime: runtime, listener: listener, connections: make(map[net.Conn]struct{})}
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	command := runtime.options.EntryCommand
	if len(command) == 0 {
		binary, err := os.Executable()
		if err != nil {
			_ = listener.Close()
			return nil, err
		}
		command = []string{binary}
	}
	args := append(slices.Clone(command), "runtime-entry", socket)
	quoted := make([]string, len(args))
	for index, arg := range args {
		quoted[index] = shellQuote(arg)
	}
	directory := filepath.Join(runtime.StateDir(), "entry-bin")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		_ = listener.Close()
		return nil, err
	}
	for _, name := range []string{"codex", "tyrs-hand-worker"} {
		body := "#!/bin/sh\nexec " + strings.Join(quoted, " ") + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(directory, name), []byte(body), 0o700); err != nil {
			_ = listener.Close()
			return nil, err
		}
	}
	gateway.wg.Add(1)
	go gateway.serve()
	return gateway, nil
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func (g *entryGateway) serve() {
	defer g.wg.Done()
	for {
		connection, err := g.listener.Accept()
		if err != nil {
			return
		}
		g.mu.Lock()
		if g.closed {
			g.mu.Unlock()
			_ = connection.Close()
			return
		}
		g.connections[connection] = struct{}{}
		g.wg.Add(1)
		g.mu.Unlock()
		go func() {
			defer g.wg.Done()
			defer connection.Close()
			defer func() { g.mu.Lock(); delete(g.connections, connection); g.mu.Unlock() }()
			g.handle(connection)
		}()
	}
}

func (g *entryGateway) handle(connection net.Conn) {
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReaderSize(connection, 16*1024)
	line, err := reader.ReadSlice('\n')
	var args []string
	if err != nil || json.Unmarshal(line, &args) != nil {
		return
	}
	_ = connection.SetReadDeadline(time.Time{})
	info := g.runtime.Info()
	reply := entryReply{}
	switch {
	case slices.Equal(args, []string{"--version"}), slices.Equal(args, []string{"-V"}):
		reply.Data = "codex-cli " + info.ProtocolVersion + "\n"
	case slices.Equal(args, []string{"runtime", "info"}):
		data, _ := json.Marshal(info)
		reply.Data = string(data) + "\n"
	case slices.Equal(args, []string{"app-server", "daemon", "start"}):
		if info.Status != "running" {
			reply.Error = "运行时不可用"
		}
	case slices.Equal(args, []string{"app-server", "proxy"}):
		if info.Status != "running" {
			reply.Error = "运行时不可用"
		} else {
			reply.Proxy = true
		}
	default:
		reply.Error = "此 SSH 入口只支持版本探测、runtime info、app-server daemon start 和 app-server proxy"
	}
	if json.NewEncoder(connection).Encode(reply) != nil || !reply.Proxy {
		return
	}
	// 握手后剩余字节必须保留，避免合并写入的 WebSocket Upgrade 丢失。
	_ = g.runtime.ServeDesktop(&bufferedEntryConn{Conn: connection, reader: reader})
}

type bufferedEntryConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedEntryConn) Read(data []byte) (int, error) { return c.reader.Read(data) }

func (g *entryGateway) close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.closed = true
	_ = g.listener.Close()
	for connection := range g.connections {
		_ = connection.Close()
	}
	g.mu.Unlock()
	g.wg.Wait()
}

// RunEntryCommand 由当前 Worker 制品执行；固定 Socket 是包装器的一部分。
func RunEntryCommand(ctx context.Context, socket string, args []string, input io.Reader, output io.Writer) error {
	connection, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		return fmt.Errorf("连接 Worker 入口: %w", err)
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(connection).Encode(args); err != nil {
		return err
	}
	reader := bufio.NewReader(connection)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return err
	}
	var reply entryReply
	if err := json.Unmarshal(line, &reply); err != nil {
		return err
	}
	if reply.Error != "" {
		return errors.New(reply.Error)
	}
	if !reply.Proxy {
		_, err := io.WriteString(output, reply.Data)
		return err
	}
	_ = connection.SetDeadline(time.Time{})
	go func() {
		_, _ = io.Copy(connection, input)
		_ = connection.(*net.UnixConn).CloseWrite()
	}()
	_, err = io.Copy(output, reader)
	return err
}
