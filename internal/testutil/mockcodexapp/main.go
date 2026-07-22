package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/gorilla/websocket"
)

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		_, _ = os.Stdout.WriteString("codex-cli 0.142.5\n")
		return
	}
	if len(os.Args) != 4 || os.Args[1] != "app-server" || os.Args[2] != "--listen" ||
		!strings.HasPrefix(os.Args[3], "unix://") {
		_, _ = os.Stderr.WriteString("unsupported mock codex invocation\n")
		os.Exit(2)
	}
	path := strings.TrimPrefix(os.Args[3], "unix://")
	_ = os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		panic(err)
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter,
		request *http.Request,
	) {
		connection, upgradeErr := upgrader.Upgrade(response, request, nil)
		if upgradeErr != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		for {
			var input message
			if readErr := connection.ReadJSON(&input); readErr != nil {
				return
			}
			if input.Method == "initialize" {
				_ = connection.WriteJSON(map[string]any{"id": input.ID, "result": map[string]any{
					"codexHome": "/var/lib/tyrs-hand/codex", "platformFamily": "unix",
					"platformOs": "linux",
				}})
				continue
			}
			if len(input.ID) > 0 {
				_ = connection.WriteJSON(map[string]any{"id": input.ID, "error": map[string]any{
					"code": -32601, "message": "unsupported mock method",
				}})
			}
		}
	})}
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		panic(err)
	}
}
