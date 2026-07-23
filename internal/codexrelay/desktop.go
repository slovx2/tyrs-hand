package codexrelay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/slovx2/tyrs-hand/internal/codex"
)

type desktopWriter struct {
	ws    *websocket.Conn
	queue chan rpcMessage
	done  chan struct{}
	once  sync.Once
}

func (w *desktopWriter) write(message rpcMessage) error {
	select {
	case <-w.done:
		return errSessionClosed
	default:
	}
	select {
	case w.queue <- message:
		return nil
	case <-w.done:
		return errSessionClosed
	default:
		return errors.New("relay Desktop 发送 backlog 已满")
	}
}

func (w *desktopWriter) close() {
	w.once.Do(func() {
		close(w.done)
		_ = w.ws.Close()
	})
}

func (w *desktopWriter) run() {
	for {
		select {
		case message := <-w.queue:
			if err := w.ws.WriteJSON(message); err != nil {
				w.close()
				return
			}
		case <-w.done:
			return
		}
	}
}

func (r *Relay) serveDesktop(response http.ResponseWriter, request *http.Request) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	ws, err := upgrader.Upgrade(response, request, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(16 << 20)
	writer := &desktopWriter{ws: ws, queue: make(chan rpcMessage, r.options.EventBacklog),
		done: make(chan struct{})}
	go writer.run()
	s, err := r.addSession(RoleDesktop, writer.write, nil)
	if err != nil {
		writer.close()
		return
	}
	defer func() {
		r.removeSession(s)
		writer.close()
	}()
	for {
		_, payload, readErr := ws.ReadMessage()
		if readErr != nil {
			return
		}
		var message rpcMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			_ = writer.write(rpcMessage{Error: &rpcError{Code: -32700, Message: "invalid JSON"}})
			continue
		}
		if message.Method == "" && len(message.ID) > 0 {
			// 已被另一端抢先解决的 Server Request 迟到答案按幂等响应忽略。
			s.resolve(message)
			continue
		}
		if message.Method == "" {
			_ = writer.write(rpcMessage{ID: message.ID,
				Error: &rpcError{Code: -32600, Message: "invalid request"}})
			continue
		}
		if len(message.ID) == 0 {
			if err := r.routeNotification(s, message.Method, message.Params); err != nil {
				return
			}
			continue
		}
		go r.handleDesktopCall(request.Context(), s, writer, message)
	}
}

func (r *Relay) handleDesktopCall(parent context.Context, source *session,
	writer *desktopWriter, message rpcMessage,
) {
	timeout := r.options.RequestTimeout
	if message.Method == "thread/archive" {
		timeout = r.options.LifecycleRequestTimeout
	}
	ctx, cancel := requestContext(parent, timeout)
	defer cancel()
	result, err := r.routeCall(ctx, source, message.Method, message.Params)
	if err == nil {
		_ = writer.write(rpcMessage{ID: message.ID, Result: result})
		return
	}
	responseError := &rpcError{Code: -32000, Message: err.Error()}
	var protocolErr *ProtocolError
	if errors.As(err, &protocolErr) {
		responseError.Code = protocolErr.Code
		responseError.Message = protocolErr.Message
		responseError.Data = protocolErr.Data
	}
	var upstreamErr *codex.RPCError
	if errors.As(err, &upstreamErr) {
		responseError.Code = upstreamErr.Code
		responseError.Message = upstreamErr.Message
		responseError.Data = upstreamErr.Data
	}
	_ = writer.write(rpcMessage{ID: message.ID, Error: responseError})
}
