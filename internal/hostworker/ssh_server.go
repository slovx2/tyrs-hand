package hostworker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"github.com/creack/pty"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

type AuthorizedClient struct {
	ID        string
	PublicKey ssh.PublicKey
}

type SSHOptions struct {
	ListenAddr        string
	HostKeyFile       string
	Home              string
	CodexHome         string
	Shell             string
	AuthorizedClients []AuthorizedClient
	Runtime           DesktopServer
	BrowserProxy      func(context.Context, io.ReadWriteCloser) error
	Logger            *zap.Logger
}

type DesktopServer interface {
	ServeDesktop(net.Conn) error
}

type SSHServer struct {
	options  SSHOptions
	listener net.Listener
	config   *ssh.ServerConfig

	mu          sync.Mutex
	connections map[*ssh.ServerConn]struct{}
	closed      bool
	wg          sync.WaitGroup
}

func StartSSHServer(ctx context.Context, options SSHOptions) (*SSHServer, error) {
	if options.ListenAddr == "" || options.HostKeyFile == "" || options.Home == "" ||
		options.CodexHome == "" || options.Runtime == nil {
		return nil, errors.New("SSH Server 配置不完整")
	}
	if options.Shell == "" {
		options.Shell = "/bin/sh"
	}
	if options.Logger == nil {
		options.Logger = zap.NewNop()
	}
	signer, err := loadOrCreateHostKey(options.HostKeyFile)
	if err != nil {
		return nil, err
	}
	clients := make(map[string]string, len(options.AuthorizedClients))
	for _, client := range options.AuthorizedClients {
		clients[string(client.PublicKey.Marshal())] = client.ID
	}
	configuration := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			clientID, ok := clients[string(key.Marshal())]
			if !ok {
				return nil, errors.New("SSH 公钥未授权")
			}
			return &ssh.Permissions{Extensions: map[string]string{"client-id": clientID}}, nil
		},
		MaxAuthTries: 3,
	}
	configuration.AddHostKey(signer)
	listener, err := net.Listen("tcp", options.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("监听 Worker SSH: %w", err)
	}
	server := &SSHServer{options: options, listener: listener, config: configuration,
		connections: make(map[*ssh.ServerConn]struct{})}
	server.wg.Add(1)
	go server.serve(ctx)
	return server, nil
}

func (s *SSHServer) Addr() net.Addr { return s.listener.Addr() }

func (s *SSHServer) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.wg.Wait()
		return nil
	}
	s.closed = true
	_ = s.listener.Close()
	for connection := range s.connections {
		_ = connection.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return nil
}

func (s *SSHServer) serve(ctx context.Context) {
	defer s.wg.Done()
	go func() {
		<-ctx.Done()
		_ = s.Close()
	}()
	for {
		raw, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go s.handleConnection(raw)
	}
}

func (s *SSHServer) handleConnection(raw net.Conn) {
	defer s.wg.Done()
	connection, channels, requests, err := ssh.NewServerConn(raw, s.config)
	if err != nil {
		_ = raw.Close()
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = connection.Close()
		return
	}
	s.connections[connection] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.connections, connection)
		s.mu.Unlock()
		_ = connection.Close()
	}()
	go ssh.DiscardRequests(requests)
	for request := range channels {
		if request.ChannelType() != "session" {
			_ = request.Reject(ssh.Prohibited, "Worker 禁止 SSH 转发")
			continue
		}
		channel, channelRequests, err := request.Accept()
		if err != nil {
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleSession(channel, channelRequests)
		}()
	}
}

type sshSessionState struct {
	environment map[string]string
	term        string
	columns     uint32
	rows        uint32
	started     bool
	process     *os.File
}

func (s *SSHServer) handleSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer func() { _ = channel.Close() }()
	state := &sshSessionState{environment: make(map[string]string), columns: 80, rows: 24}
	for request := range requests {
		switch request.Type {
		case "env":
			var input struct{ Name, Value string }
			if ssh.Unmarshal(request.Payload, &input) == nil && !state.started {
				state.environment[input.Name] = input.Value
				_ = request.Reply(true, nil)
			} else {
				_ = request.Reply(false, nil)
			}
		case "pty-req":
			var input struct {
				Term                         string
				Columns, Rows, Width, Height uint32
				Modes                        string
			}
			if ssh.Unmarshal(request.Payload, &input) == nil && !state.started {
				state.term, state.columns, state.rows = input.Term, input.Columns, input.Rows
				_ = request.Reply(true, nil)
			} else {
				_ = request.Reply(false, nil)
			}
		case "window-change":
			var input struct{ Columns, Rows, Width, Height uint32 }
			if ssh.Unmarshal(request.Payload, &input) == nil {
				state.columns, state.rows = input.Columns, input.Rows
				if state.process != nil {
					_ = pty.Setsize(state.process, &pty.Winsize{Cols: uint16(input.Columns), Rows: uint16(input.Rows)})
				}
			}
		case "subsystem":
			var input struct{ Name string }
			if ssh.Unmarshal(request.Payload, &input) == nil && input.Name == "sftp" && !state.started {
				state.started = true
				_ = request.Reply(true, nil)
				s.serveSFTP(channel)
				return
			}
			_ = request.Reply(false, nil)
		case "exec":
			var input struct{ Command string }
			if ssh.Unmarshal(request.Payload, &input) != nil || state.started {
				_ = request.Reply(false, nil)
				continue
			}
			state.started = true
			_ = request.Reply(true, nil)
			s.runCommand(channel, state, input.Command)
			return
		case "shell":
			if state.started {
				_ = request.Reply(false, nil)
				continue
			}
			state.started = true
			_ = request.Reply(true, nil)
			s.runCommand(channel, state, "")
			return
		default:
			_ = request.Reply(false, nil)
		}
	}
}
