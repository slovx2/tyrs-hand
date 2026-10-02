package hostworker

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"

	"github.com/slovx2/codex-harness-adapter/sshserver"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"go.uber.org/zap"
)

type AuthorizedClient = sshserver.AuthorizedClient
type ClientAuthorization = sshserver.ClientAuthorization
type DesktopServer = sshserver.DesktopServer

func NewClientAuthorization(clients []AuthorizedClient) *ClientAuthorization {
	return sshserver.NewClientAuthorization(clients)
}

type SSHOptions struct {
	ListenAddr        string
	HostKeyFile       string
	Home              string
	CodexHome         string
	Shell             string
	AuthorizedClients []AuthorizedClient
	Authorization     *ClientAuthorization
	Runtime           DesktopServer
	RuntimeInfo       func() RuntimeInfo
	BrowserProxy      func(context.Context, io.ReadWriteCloser) error
	Logger            *zap.Logger
}

// Worker 只注入业务策略，SSH 协议与连接生命周期由独立库实现。
type SSHServer struct {
	*sshserver.SSHServer
	options SSHOptions
	wg      sync.WaitGroup
}

func (s *SSHServer) Close() error {
	err := s.SSHServer.Close()
	// 通用服务已停止接收转发请求，再等待本体持有的 OAuth 回调结束。
	s.wg.Wait()
	return err
}

func StartSSHServer(ctx context.Context, options SSHOptions) (*SSHServer, error) {
	s := &SSHServer{options: options}
	shared := sshserver.SSHOptions{
		ListenAddr: options.ListenAddr, HostKeyFile: options.HostKeyFile,
		Home: options.Home, CodexHome: options.CodexHome, Shell: options.Shell,
		AuthorizedClients: options.AuthorizedClients, Authorization: options.Authorization,
		Runtime: options.Runtime, Logger: options.Logger, Command: s.command,
		Forward: s.handleOAuthForward,
	}
	if runtime, ok := options.Runtime.(*Runtime); ok {
		shared.EntryBin = runtime.EntryBin()
		shared.Environment = func() []string {
			environment := appServerEnvironment(runtime.options.Environment)
			values := map[string]string{}
			if runtime.options.Engine == runtimeidentity.Pi {
				environment = piEnvironment(runtime.options.Environment)
				values["CHA_PI_HOME"] = runtime.StateDir()
			}
			if runtime.options.Engine == runtimeidentity.Claude {
				values["CHA_CLAUDE_HOME"] = runtime.StateDir()
			}
			return replaceEnvironment(environment, values)
		}
	} else {
		shared.Environment = os.Environ
	}
	server, err := sshserver.StartSSHServer(ctx, shared)
	if err != nil {
		return nil, err
	}
	s.SSHServer = server
	return s, nil
}

func (s *SSHServer) command(ctx context.Context, command string, channel io.ReadWriteCloser) (bool, uint32) {
	if s.options.RuntimeInfo != nil {
		info := s.options.RuntimeInfo()
		switch command {
		case "tyrs-hand-worker runtime info":
			if err := json.NewEncoder(channel).Encode(info); err != nil {
				return true, 1
			}
			return true, 0
		case "codex --version", "codex -V":
			_, err := io.WriteString(channel, "codex-cli "+info.CodexVersion()+"\n")
			if err != nil {
				return true, 1
			}
			return true, 0
		case "codex app-server daemon start":
			if info.Status != "running" {
				return true, 1
			}
			return true, 0
		}
	}
	if command == "tyrs-hand-worker browser proxy" {
		if s.options.BrowserProxy == nil {
			return true, 127
		}
		if err := s.options.BrowserProxy(ctx, channel); err != nil {
			return true, 1
		}
		return true, 0
	}
	return false, 0
}
