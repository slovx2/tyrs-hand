package hostworker

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/pkg/sftp"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

func (s *SSHServer) serveSFTP(channel ssh.Channel) {
	server, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(s.options.Home))
	if err != nil {
		s.writeExit(channel, 1)
		return
	}
	err = server.Serve()
	_ = server.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		s.writeExit(channel, 1)
		return
	}
	s.writeExit(channel, 0)
}

func (s *SSHServer) runCommand(channel ssh.Channel, state *sshSessionState, command string) {
	trimmed := strings.TrimSpace(command)
	switch trimmed {
	case "tyrs-hand-worker browser proxy":
		if s.options.BrowserProxy == nil {
			s.writeExit(channel, 127)
			return
		}
		err := s.options.BrowserProxy(context.Background(), channel)
		if err != nil {
			s.writeExit(channel, 1)
			return
		}
		s.writeExit(channel, 0)
		return
	}
	handshake, desktopProxy, err := parseDesktopProxyCommand(trimmed)
	if err != nil {
		s.options.Logger.Warn("拒绝无法识别的 Codex Desktop SSH 命令", zap.Error(err))
		s.writeExit(channel, 127)
		return
	}
	if desktopProxy {
		if len(handshake) > 0 {
			if _, err := channel.Write(handshake); err != nil {
				s.writeExit(channel, 1)
				return
			}
		}
		err = s.serveDesktop(channel)
		if err != nil {
			s.options.Logger.Warn("Codex Desktop SSH 会话停止", zap.Error(err))
			s.writeExit(channel, 1)
			return
		}
		s.writeExit(channel, 0)
		return
	}

	s.runProcess(channel, state, command, channel)
}

func (s *SSHServer) runShell(channel ssh.Channel, state *sshSessionState) {
	if state.term != "" {
		s.runProcess(channel, state, "", channel)
		return
	}
	reader := bufio.NewReaderSize(channel, 64*1024)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		s.writeExit(channel, 1)
		return
	}
	handshake, desktopProxy, parseErr := parseDesktopProxyCommand(strings.TrimSpace(line))
	if parseErr != nil {
		s.options.Logger.Warn("拒绝无法识别的 Codex Desktop SSH shell 命令", zap.Error(parseErr))
		s.writeExit(channel, 127)
		return
	}
	if desktopProxy {
		if len(handshake) > 0 {
			if _, err := channel.Write(handshake); err != nil {
				s.writeExit(channel, 1)
				return
			}
		}
		if err := s.serveDesktopInput(channel, reader); err != nil {
			s.options.Logger.Warn("Codex Desktop SSH shell 会话停止", zap.Error(err))
			s.writeExit(channel, 1)
			return
		}
		s.writeExit(channel, 0)
		return
	}
	s.runProcess(channel, state, "", io.MultiReader(strings.NewReader(line), reader))
}

func (s *SSHServer) runProcess(channel ssh.Channel, state *sshSessionState, command string, input io.Reader) {
	arguments := []string(nil)
	if strings.TrimSpace(command) != "" {
		arguments = []string{"-lc", command}
	}
	process := exec.Command(s.options.Shell, arguments...)
	process.Dir = s.options.Home
	values := map[string]string{"HOME": s.options.Home, "CODEX_HOME": s.options.CodexHome}
	for name, value := range state.environment {
		if allowedSSHEnvironment(name) {
			values[name] = value
		}
	}
	if state.term != "" {
		values["TERM"] = state.term
	}
	process.Env = replaceEnvironment(os.Environ(), values)
	if state.term != "" {
		s.runPTY(channel, state, process)
		return
	}
	process.Stdin, process.Stdout, process.Stderr = input, channel, channel.Stderr()
	err := process.Run()
	s.writeExit(channel, exitStatus(err))
}

func (s *SSHServer) serveDesktop(channel ssh.Channel) error {
	return s.serveDesktopInput(channel, channel)
}

func (s *SSHServer) serveDesktopInput(channel ssh.Channel, input io.Reader) error {
	// SSH Channel 没有 net.Conn 所需的地址和 deadline，并且客户端关闭 stdin 时
	// 必须只向 App Server 传播读 EOF，不能截断仍在返回的数据。
	serverConnection, proxyConnection := newDesktopPipe()
	type proxyResult struct {
		source string
		err    error
	}
	finished := make(chan proxyResult, 3)
	go func() {
		finished <- proxyResult{source: "runtime", err: s.options.Runtime.ServeDesktop(serverConnection)}
	}()
	go func() {
		_, err := io.Copy(proxyConnection, input)
		closeErr := proxyConnection.CloseWrite()
		if err == nil && closeErr != nil && !errors.Is(closeErr, io.ErrClosedPipe) &&
			!errors.Is(closeErr, net.ErrClosed) {
			err = closeErr
		}
		finished <- proxyResult{source: "input", err: err}
	}()
	go func() {
		_, err := io.Copy(channel, proxyConnection)
		finished <- proxyResult{source: "output", err: err}
	}()
	var result proxyResult
	var runtimeErr error
	for {
		result = <-finished
		if result.source == "input" && (result.err == nil || errors.Is(result.err, io.EOF) ||
			errors.Is(result.err, net.ErrClosed)) {
			// SSH exec 可以先结束输入、再继续读取 App Server 输出，不能据此截断反向数据。
			continue
		}
		if result.source == "runtime" {
			runtimeErr = result.err
			_ = serverConnection.Close()
			for result.source != "output" {
				result = <-finished
			}
		}
		break
	}
	_ = serverConnection.Close()
	_ = proxyConnection.Close()
	if runtimeErr != nil && !errors.Is(runtimeErr, io.EOF) && !errors.Is(runtimeErr, net.ErrClosed) {
		return runtimeErr
	}
	if errors.Is(result.err, io.EOF) || errors.Is(result.err, net.ErrClosed) {
		return nil
	}
	return result.err
}

type desktopPipeConnection struct {
	reader    net.Conn
	writer    net.Conn
	local     net.Addr
	remote    net.Addr
	closeOnce sync.Once
}

type desktopPipeAddress string

func newDesktopPipe() (*desktopPipeConnection, *desktopPipeConnection) {
	serverReader, proxyWriter := net.Pipe()
	proxyReader, serverWriter := net.Pipe()
	server := &desktopPipeConnection{reader: serverReader, writer: serverWriter,
		local: desktopPipeAddress("worker-runtime"), remote: desktopPipeAddress("ssh-client")}
	proxy := &desktopPipeConnection{reader: proxyReader, writer: proxyWriter,
		local: desktopPipeAddress("ssh-client"), remote: desktopPipeAddress("worker-runtime")}
	return server, proxy
}

func (c *desktopPipeConnection) Read(value []byte) (int, error) {
	return c.reader.Read(value)
}

func (c *desktopPipeConnection) Write(value []byte) (int, error) {
	return c.writer.Write(value)
}

func (c *desktopPipeConnection) CloseWrite() error { return c.writer.Close() }

func (c *desktopPipeConnection) Close() error {
	var result error
	c.closeOnce.Do(func() {
		result = errors.Join(c.reader.Close(), c.writer.Close())
	})
	return result
}

func (c *desktopPipeConnection) LocalAddr() net.Addr  { return c.local }
func (c *desktopPipeConnection) RemoteAddr() net.Addr { return c.remote }

func (c *desktopPipeConnection) SetDeadline(deadline time.Time) error {
	return errors.Join(c.reader.SetReadDeadline(deadline), c.writer.SetWriteDeadline(deadline))
}

func (c *desktopPipeConnection) SetReadDeadline(deadline time.Time) error {
	return c.reader.SetReadDeadline(deadline)
}

func (c *desktopPipeConnection) SetWriteDeadline(deadline time.Time) error {
	return c.writer.SetWriteDeadline(deadline)
}

func (a desktopPipeAddress) Network() string { return "ssh-pipe" }
func (a desktopPipeAddress) String() string  { return string(a) }

func (s *SSHServer) runPTY(channel ssh.Channel, state *sshSessionState, process *exec.Cmd) {
	terminal, err := pty.StartWithSize(process, &pty.Winsize{
		Cols: uint16(state.columns), Rows: uint16(state.rows),
	})
	if err != nil {
		s.writeExit(channel, 1)
		return
	}
	state.process = terminal
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(terminal, channel)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(channel, terminal)
		done <- struct{}{}
	}()
	err = process.Wait()
	_ = terminal.Close()
	<-done
	s.writeExit(channel, exitStatus(err))
}

func (s *SSHServer) writeExit(channel ssh.Channel, status uint32) {
	_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
}

func allowedSSHEnvironment(name string) bool {
	switch name {
	case "LANG", "LC_ALL", "LC_CTYPE", "COLORTERM", "TERM_PROGRAM":
		return true
	default:
		return strings.HasPrefix(name, "LC_")
	}
}

func exitStatus(err error) uint32 {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() >= 0 {
		return uint32(exit.ExitCode())
	}
	return 1
}

func loadOrCreateHostKey(path string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return ssh.ParsePrivateKey(data)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})
	if err := atomicWriteSecret(path, block); err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(privateKey)
}

func atomicWriteSecret(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".host-key-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
