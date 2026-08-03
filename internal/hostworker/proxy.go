package hostworker

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const browserAgentRegistrationLimit = 64 << 10

func BrowserAgentProxy(address, token string) func(context.Context, io.ReadWriteCloser) error {
	return func(ctx context.Context, stream io.ReadWriteCloser) error {
		if address == "" {
			return errors.New("browser Bridge 地址未配置")
		}
		if token == "" {
			return errors.New("browser Agent Workspace Token 未配置")
		}
		upstream, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
		if err != nil {
			return err
		}
		defer func() { _ = upstream.Close() }()
		registration, err := browserAgentRegistrationFrame(token)
		if err != nil {
			return err
		}
		if _, err := upstream.Write(registration); err != nil {
			return fmt.Errorf("注册 Desktop Browser Agent: %w", err)
		}
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

func browserAgentRegistrationFrame(token string) ([]byte, error) {
	payload, err := json.Marshal(map[string]string{"type": "register", "token": token})
	if err != nil {
		return nil, err
	}
	if len(payload) > browserAgentRegistrationLimit {
		return nil, errors.New("browser Agent 注册帧过大")
	}
	frame := bytes.NewBuffer(make([]byte, 0, len(payload)+4))
	if err := binary.Write(frame, binary.BigEndian, uint32(len(payload))); err != nil {
		return nil, err
	}
	_, _ = frame.Write(payload)
	return frame.Bytes(), nil
}
