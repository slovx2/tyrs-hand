package sshtransport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"golang.org/x/crypto/ssh"
)

type runtimeInfo struct {
	runtimeidentity.Identity
	ProtocolVersion string   `json:"protocolVersion"`
	Status          string   `json:"status"`
	Capabilities    []string `json:"capabilities"`
	ReleaseReady    bool     `json:"releaseReady"`
}

func readRuntimeInfo(client *ssh.Client) (runtimeInfo, error) {
	var info runtimeInfo
	// 超时关闭此 SSH 连接，确保失联入口不能挂起手机连接流程。
	timer := time.AfterFunc(10*time.Second, func() { _ = client.Close() })
	defer timer.Stop()
	data, err := commandOutput(client, "tyrs-hand-worker runtime info")
	if err != nil {
		return info, fmt.Errorf("读取 Worker 运行时身份: %w", err)
	}
	if err := json.Unmarshal([]byte(data), &info); err != nil {
		return info, fmt.Errorf("解析 Worker 运行时身份: %w", err)
	}
	if err := info.Validate(); err != nil {
		return info, err
	}
	if info.ProtocolVersion != codex.RequiredVersion {
		return info, errors.New("不匹配的 Worker 运行时协议版本")
	}
	if info.Status != "running" && info.Status != "unavailable" && info.Status != "stopped" {
		return info, errors.New("无效的 Worker 运行时状态")
	}
	return info, nil
}

// InspectRuntime 用于保存 SSH 连接前发现引擎，不启动 App Server 或模型请求。
func InspectRuntime(host string, port int, user, privateKey, passphrase, expectedHostFingerprint string) (string, error) {
	client, err := connectSSH(context.Background(), sshOptions{host: host, port: port, user: user,
		privateKey: privateKey, passphrase: passphrase, expectedHostFingerprint: expectedHostFingerprint})
	if err != nil {
		return "", err
	}
	defer func() { _ = client.Close() }()
	info, err := readRuntimeInfo(client)
	if err != nil {
		return "", err
	}
	return marshalJSON(info)
}
