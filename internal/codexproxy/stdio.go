package codexproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
)

func ServeStdio(ctx context.Context, socketPath string) error {
	return Relay(ctx, socketPath, os.Stdin, os.Stdout)
}

func Relay(ctx context.Context, socketPath string, input io.Reader, output io.Writer) error {
	if socketPath == "" {
		return errors.New("缺少 Codex Desktop Proxy Relay Socket")
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	go func() {
		<-ctx.Done()
		_ = connection.Close()
	}()

	var wait sync.WaitGroup
	wait.Add(2)
	errorsCh := make(chan error, 2)
	go func() {
		defer wait.Done()
		_, copyErr := io.Copy(connection, input)
		if unix, ok := connection.(*net.UnixConn); ok {
			_ = unix.CloseWrite()
		}
		errorsCh <- copyErr
	}()
	go func() {
		defer wait.Done()
		_, copyErr := io.Copy(output, connection)
		errorsCh <- copyErr
	}()
	wait.Wait()
	close(errorsCh)
	for copyErr := range errorsCh {
		if copyErr != nil && !errors.Is(copyErr, net.ErrClosed) && !errors.Is(copyErr, io.EOF) {
			return copyErr
		}
	}
	return nil
}
