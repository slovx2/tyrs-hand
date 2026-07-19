package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

type writeError struct {
	written int
	cause   error
}

func (e *writeError) Error() string { return e.cause.Error() }
func (e *writeError) Unwrap() error { return e.cause }

func writeRequestState(err error) RequestState {
	var value *writeError
	if errors.As(err, &value) && value.written == 0 {
		return RequestNotSent
	}
	return RequestUnknown
}

func (c *Client) write(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	written := 0
	for written < len(data) {
		count, writeErr := c.stdin.Write(data[written:])
		written += count
		if writeErr != nil {
			return &writeError{written: written, cause: writeErr}
		}
		if count == 0 {
			return &writeError{written: written, cause: io.ErrShortWrite}
		}
	}
	return nil
}

func (c *Client) readLoop(reader io.Reader) {
	defer close(c.events)
	if c.readDone != nil {
		defer close(c.readDone)
	}
	buffered := bufio.NewReaderSize(reader, 64*1024)
	maxFrameBytes := c.options.MaxFrameBytes
	if maxFrameBytes <= 0 {
		maxFrameBytes = 16 << 20
	}
	for {
		frame, err := readFrame(buffered, maxFrameBytes)
		if err == nil && len(frame) > 0 {
			if handleErr := c.handleFrame(frame); handleErr != nil {
				c.setReadError(handleErr)
				if c.process != nil {
					_ = c.process.Kill()
				}
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				c.setReadError(err)
			} else if len(frame) > 0 {
				c.setReadError(errors.New("codex stdout 在完整 JSON 帧结束前 EOF"))
			}
			return
		}
	}
}

func readFrame(reader *bufio.Reader, maxBytes int) ([]byte, error) {
	var frame []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(frame)+len(part) > maxBytes {
			return nil, fmt.Errorf("codex JSON 帧超过 %d 字节限制", maxBytes)
		}
		frame = append(frame, part...)
		if err == nil {
			return bytesTrimLine(frame), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return bytesTrimLine(frame), err
	}
}

func bytesTrimLine(value []byte) []byte {
	return []byte(strings.TrimSuffix(strings.TrimSuffix(string(value), "\n"), "\r"))
}

func (c *Client) handleFrame(frame []byte) error {
	if len(frame) == 0 {
		return nil
	}
	var message rpcMessage
	if err := json.Unmarshal(frame, &message); err != nil {
		return fmt.Errorf("codex stdout 包含非法 JSON: %w", err)
	}
	if len(message.ID) > 0 && message.Method != "" {
		go c.handleServerRequest(message)
		return nil
	}
	if len(message.ID) > 0 {
		c.deliverResponse(message)
		return nil
	}
	if message.Method == "" {
		return errors.New("codex stdout 包含未知 JSON-RPC 消息")
	}
	select {
	case c.events <- Event{Method: message.Method, Params: message.Params}:
		return nil
	default:
		return fmt.Errorf("codex 事件 backlog 超过 %d 条", cap(c.events))
	}
}

func (c *Client) handleServerRequest(message rpcMessage) {
	if message.Method != "item/tool/call" || c.options.ToolHandler == nil {
		_ = c.write(responseEnvelope{ID: message.ID,
			Error: &rpcError{Code: -32601, Message: "unsupported server request"}})
		return
	}
	var request ToolCallRequest
	if err := json.Unmarshal(message.Params, &request); err != nil {
		_ = c.write(responseEnvelope{ID: message.ID,
			Error: &rpcError{Code: -32602, Message: "invalid tool call"}})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.options.ToolTimeout)
	defer cancel()
	type outcome struct {
		result ToolCallResult
		err    error
	}
	completed := make(chan outcome, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				completed <- outcome{err: fmt.Errorf("tool handler panic: %v", recovered)}
			}
		}()
		result, err := c.options.ToolHandler(ctx, request)
		completed <- outcome{result: result, err: err}
	}()
	var result ToolCallResult
	select {
	case value := <-completed:
		result = value.result
		if value.err != nil {
			result = TextToolResult(value.err.Error(), false)
		}
	case <-ctx.Done():
		result = TextToolResult("tool handler timeout", false)
	}
	if err := c.write(responseEnvelope{ID: message.ID, Result: result}); err != nil {
		c.setReadError(fmt.Errorf("写回 Codex Tool Result: %w", err))
		_ = c.process.Kill()
	}
}

func (c *Client) deliverResponse(message rpcMessage) {
	id, err := strconv.ParseInt(string(message.ID), 10, 64)
	if err != nil {
		return
	}
	c.mu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ch != nil {
		ch <- message
	}
}

func (c *Client) waitLoop() {
	processErr := c.process.Wait()
	<-c.readDone
	c.mu.Lock()
	readErr := c.readErr
	c.mu.Unlock()
	if c.closing.Load() {
		c.fail(errClientClosed)
	} else if readErr != nil {
		c.fail(readErr)
	} else if processErr != nil {
		c.fail(fmt.Errorf("当前 Codex App Server 退出: %w", processErr))
	} else {
		c.fail(io.EOF)
	}
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
		for id, ch := range c.pending {
			delete(c.pending, id)
			close(ch)
		}
		close(c.done)
	}
	c.mu.Unlock()
}

func (c *Client) setReadError(err error) {
	c.mu.Lock()
	if c.readErr == nil {
		c.readErr = err
	}
	c.mu.Unlock()
}

func (c *Client) stderrLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		c.options.Logger.Debug("Codex", zap.String("line", scanner.Text()))
	}
}

func cleanEnvironment(extra []string) []string {
	allowed := map[string]bool{"PATH": true, "LANG": true, "LC_ALL": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true,
		"SSL_CERT_FILE": true, "CODEX_CA_CERTIFICATE": true}
	values := make(map[string]string)
	for _, item := range append(os.Environ(), extra...) {
		key, _, ok := strings.Cut(item, "=")
		if ok && (allowed[key] || containsEnvironmentKey(extra, key)) {
			values[key] = item
		}
	}
	result := make([]string, 0, len(values))
	for _, item := range values {
		result = append(result, item)
	}
	return result
}

func containsEnvironmentKey(values []string, key string) bool {
	for _, item := range values {
		if strings.HasPrefix(item, key+"=") {
			return true
		}
	}
	return false
}
