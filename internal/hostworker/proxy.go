package hostworker

import (
	"context"
	"errors"
	"io"
	"net"
	"time"
)

func TCPProxy(address string) func(context.Context, io.ReadWriteCloser) error {
	return func(ctx context.Context, stream io.ReadWriteCloser) error {
		if address == "" {
			return errors.New("browser Bridge 地址未配置")
		}
		upstream, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
		if err != nil {
			return err
		}
		defer func() { _ = upstream.Close() }()
		finished := make(chan error, 2)
		copyStream := func(destination io.Writer, source io.Reader) {
			_, copyErr := io.Copy(destination, source)
			finished <- copyErr
		}
		go copyStream(upstream, stream)
		go copyStream(stream, upstream)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case copyErr := <-finished:
			return copyErr
		}
	}
}
