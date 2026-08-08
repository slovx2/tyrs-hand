package sshtransport

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type appServerEndpoint struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

type loopbackTunnel struct {
	profileID string
	token     string
	listener  net.Listener
	ssh       *ssh.Client
	ctx       context.Context
	cancel    context.CancelFunc

	mu         sync.Mutex
	connection net.Conn
	closeOnce  sync.Once
}

var tunnelRegistry = struct {
	sync.Mutex
	items map[string]*loopbackTunnel
}{items: make(map[string]*loopbackTunnel)}

func OpenAppServer(profileID, host string, port int, user, privateKey, passphrase,
	expectedHostFingerprint string,
) (string, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return "", errors.New("SSH profileId 不能为空")
	}
	client, err := connectSSH(context.Background(), sshOptions{
		host: host, port: port, user: user, privateKey: privateKey, passphrase: passphrase,
		expectedHostFingerprint: expectedHostFingerprint,
	})
	if err != nil {
		return "", err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		_ = client.Close()
		return "", err
	}
	token, err := randomLoopbackToken()
	if err != nil {
		_ = listener.Close()
		_ = client.Close()
		return "", err
	}
	ctx, cancel := context.WithCancel(context.Background())
	tunnel := &loopbackTunnel{profileID: profileID, token: token, listener: listener,
		ssh: client, ctx: ctx, cancel: cancel}
	tunnelRegistry.Lock()
	previous := tunnelRegistry.items[profileID]
	tunnelRegistry.items[profileID] = tunnel
	tunnelRegistry.Unlock()
	if previous != nil {
		previous.close()
	}
	go tunnel.serve()
	return marshalJSON(appServerEndpoint{
		URL: "ws://" + listener.Addr().String() + "/" + token, Token: token,
	})
}

func Close(profileID string) {
	tunnelRegistry.Lock()
	tunnel := tunnelRegistry.items[profileID]
	if tunnel != nil {
		delete(tunnelRegistry.items, profileID)
	}
	tunnelRegistry.Unlock()
	if tunnel != nil {
		tunnel.close()
	}
}

func (t *loopbackTunnel) serve() {
	defer t.finish()
	if listener, ok := t.listener.(*net.TCPListener); ok {
		_ = listener.SetDeadline(time.Now().Add(45 * time.Second))
	}
	for {
		connection, err := t.listener.Accept()
		if err != nil {
			return
		}
		_ = connection.SetReadDeadline(time.Now().Add(10 * time.Second))
		header, valid := readLegalUpgrade(connection, "/"+t.token)
		if !valid {
			_ = connection.Close()
			continue
		}
		_ = t.listener.Close()
		_ = connection.SetDeadline(time.Time{})
		t.mu.Lock()
		t.connection = connection
		t.mu.Unlock()
		if err = relayAppServerProxy(t.ctx, t.ssh, connection, header); err != nil {
			writeLoopbackFailure(connection)
		}
		_ = connection.Close()
		return
	}
}

func (t *loopbackTunnel) close() {
	t.closeOnce.Do(func() {
		t.cancel()
		_ = t.listener.Close()
		t.mu.Lock()
		connection := t.connection
		t.mu.Unlock()
		if connection != nil {
			_ = connection.Close()
		}
		_ = t.ssh.Close()
	})
}

func (t *loopbackTunnel) finish() {
	tunnelRegistry.Lock()
	if tunnelRegistry.items[t.profileID] == t {
		delete(tunnelRegistry.items, t.profileID)
	}
	tunnelRegistry.Unlock()
	t.close()
}

func randomLoopbackToken() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func readLegalUpgrade(connection net.Conn, expectedPath string) ([]byte, bool) {
	const maximumHeader = 32 * 1024
	header := make([]byte, 0, 1024)
	one := []byte{0}
	for len(header) < maximumHeader {
		if _, err := io.ReadFull(connection, one); err != nil {
			return nil, false
		}
		header = append(header, one[0])
		if len(header) >= 4 && bytes.Equal(header[len(header)-4:], []byte("\r\n\r\n")) {
			break
		}
	}
	if len(header) < 4 || len(header) >= maximumHeader {
		return nil, false
	}
	request, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(header)))
	if err != nil {
		return nil, false
	}
	_ = request.Body.Close()
	return header, request.Method == http.MethodGet && request.URL.Path == expectedPath &&
		headerHasToken(request.Header.Get("Connection"), "upgrade") &&
		strings.EqualFold(request.Header.Get("Upgrade"), "websocket") &&
		request.Header.Get("Sec-WebSocket-Key") != "" &&
		request.Header.Get("Sec-WebSocket-Version") == "13"
}

func headerHasToken(value, token string) bool {
	for _, item := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(item), token) {
			return true
		}
	}
	return false
}

func relayAppServerProxy(ctx context.Context, client *ssh.Client, local net.Conn,
	header []byte,
) error {
	if err := relayProxyAttempt(ctx, client, local, header); err == nil {
		return nil
	}
	if err := ensureRemoteDaemon(client); err != nil {
		return err
	}
	return relayProxyAttempt(ctx, client, local, header)
}

type firstProxyRead struct {
	data []byte
	err  error
}

func relayProxyAttempt(ctx context.Context, client *ssh.Client, local net.Conn,
	header []byte,
) error {
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return err
	}
	var stderr bytes.Buffer
	session.Stderr = &stderr
	if err = session.Start("codex app-server proxy"); err != nil {
		_ = session.Close()
		return err
	}
	if _, err = stdin.Write(header); err != nil {
		_ = session.Close()
		return err
	}
	first := make(chan firstProxyRead, 1)
	go func() {
		header, readErr := readProxyUpgrade(stdout)
		first <- firstProxyRead{data: header, err: readErr}
	}()
	select {
	case <-ctx.Done():
		_ = session.Close()
		return ctx.Err()
	case <-time.After(15 * time.Second):
		_ = session.Close()
		return errors.New("远端 App Server proxy 握手超时")
	case result := <-first:
		if result.err != nil {
			_ = session.Close()
			message := strings.TrimSpace(stderr.String())
			if message == "" {
				return fmt.Errorf("远端 App Server proxy 不可用: %w", result.err)
			}
			return fmt.Errorf("远端 App Server proxy 不可用: %w (%s)", result.err, message)
		}
		if _, err = local.Write(result.data); err != nil {
			_ = session.Close()
			return err
		}
	}
	copyDone := make(chan struct{}, 1)
	go func() {
		_, _ = io.Copy(stdin, local)
		_ = stdin.Close()
		copyDone <- struct{}{}
	}()
	_, copyErr := io.Copy(local, stdout)
	_ = session.Close()
	select {
	case <-copyDone:
	case <-time.After(time.Second):
	}
	return copyErr
}

func readProxyUpgrade(reader io.Reader) ([]byte, error) {
	const maximumHeader = 32 * 1024
	header := make([]byte, 0, 1024)
	one := []byte{0}
	for len(header) < maximumHeader {
		if _, err := io.ReadFull(reader, one); err != nil {
			return nil, err
		}
		header = append(header, one[0])
		if len(header) >= 4 && bytes.Equal(header[len(header)-4:], []byte("\r\n\r\n")) {
			break
		}
	}
	if len(header) < 4 || len(header) >= maximumHeader {
		return nil, errors.New("远端 App Server proxy 返回的 HTTP Upgrade 头无效")
	}
	response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(header)), nil)
	if err != nil {
		return nil, fmt.Errorf("解析远端 App Server proxy 响应: %w", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSwitchingProtocols ||
		!headerHasToken(response.Header.Get("Connection"), "upgrade") ||
		!strings.EqualFold(response.Header.Get("Upgrade"), "websocket") {
		return nil, fmt.Errorf("远端 App Server proxy 拒绝 Upgrade: %s", response.Status)
	}
	return header, nil
}

func writeLoopbackFailure(connection net.Conn) {
	_, _ = io.WriteString(connection, "HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n"+
		"Content-Length: 0\r\n\r\n")
}
