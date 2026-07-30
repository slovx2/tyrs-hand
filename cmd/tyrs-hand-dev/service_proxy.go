package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	serviceProxyMagic   = "TYSP"
	serviceProxyTimeout = 5 * time.Second
)

func serveServiceProxy(ctx context.Context, socketPath string) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o770); err != nil {
		return err
	}
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	_ = os.Chmod(socketPath, 0o660)
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			if ctx.Err() != nil || errors.Is(acceptErr, net.ErrClosed) {
				return nil
			}
			return acceptErr
		}
		go handleServiceProxyConnection(connection)
	}
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
