package sshtransport

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const requiredCodexVersion = "0.147.0"

type sshOptions struct {
	host                    string
	port                    int
	user                    string
	privateKey              string
	passphrase              string
	expectedHostFingerprint string
}

func ProbeHost(host string, port int, user string) (string, error) {
	address, err := sshAddress(host, port)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(user) == "" {
		return "", errors.New("SSH 用户不能为空")
	}
	var fingerprint string
	config := &ssh.ClientConfig{
		User: strings.TrimSpace(user),
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fingerprint = ssh.FingerprintSHA256(key)
			return nil
		},
	}
	connection, err := (&net.Dialer{Timeout: 10 * time.Second}).Dial("tcp", address)
	if err != nil {
		return "", fmt.Errorf("连接 SSH 主机: %w", err)
	}
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	clientConnection, channels, requests, handshakeErr := ssh.NewClientConn(connection, address, config)
	if clientConnection != nil {
		_ = clientConnection.Close()
	}
	_ = channels
	_ = requests
	if fingerprint != "" {
		return fingerprint, nil
	}
	return "", fmt.Errorf("读取 SSH 主机指纹: %w", handshakeErr)
}

func connectSSH(ctx context.Context, options sshOptions) (*ssh.Client, error) {
	address, err := sshAddress(options.host, options.port)
	if err != nil {
		return nil, err
	}
	user := strings.TrimSpace(options.user)
	if user == "" {
		return nil, errors.New("SSH 用户不能为空")
	}
	expected := strings.TrimSpace(options.expectedHostFingerprint)
	if expected == "" {
		return nil, errors.New("尚未确认 SSH 主机 SHA-256 指纹")
	}
	signer, err := parseSigner(options.privateKey, options.passphrase)
	if err != nil {
		return nil, err
	}
	config := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: pinnedHostKeyCallback(expected),
	}
	connection, err := (&net.Dialer{Timeout: 12 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("连接 SSH 主机: %w", err)
	}
	_ = connection.SetDeadline(time.Now().Add(15 * time.Second))
	clientConnection, channels, requests, err := ssh.NewClientConn(connection, address, config)
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("SSH 握手失败: %w", err)
	}
	_ = connection.SetDeadline(time.Time{})
	return ssh.NewClient(clientConnection, channels, requests), nil
}

func pinnedHostKeyCallback(expected string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		actual := ssh.FingerprintSHA256(key)
		if subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
			return fmt.Errorf("SSH 主机指纹已变化：期望 %s，实际 %s", expected, actual)
		}
		return nil
	}
}

func sshAddress(host string, port int) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" || port < 1 || port > 65535 {
		return "", errors.New("SSH Host 或 Port 无效")
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func commandOutput(client *ssh.Client, command string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer func() { _ = session.Close() }()
	output, err := session.CombinedOutput(command)
	return strings.TrimSpace(string(output)), err
}

func ensureRemoteDaemon(client *ssh.Client) error {
	version, err := commandOutput(client, "codex --version")
	if err != nil {
		return fmt.Errorf("读取远端 Codex 版本: %w", err)
	}
	if version != "codex-cli "+requiredCodexVersion && version != "codex "+requiredCodexVersion {
		return fmt.Errorf("远端 Codex 必须精确为 %s，当前为 %q", requiredCodexVersion, version)
	}
	if output, err := commandOutput(client, "codex app-server daemon start"); err != nil {
		return fmt.Errorf("启动远端 Codex App Server daemon: %w (%s)", err, output)
	}
	return nil
}
