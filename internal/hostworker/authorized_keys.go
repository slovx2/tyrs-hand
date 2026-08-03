package hostworker

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
)

func LoadAuthorizedClients(path string) ([]AuthorizedClient, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("worker SSH 授权公钥文件不存在: %s", path)
	}
	if err != nil {
		return nil, err
	}
	return ParseAuthorizedClients(data)
}

func ParseAuthorizedClients(data []byte) ([]AuthorizedClient, error) {
	remaining := bytes.TrimSpace(data)
	clients := make([]AuthorizedClient, 0)
	seen := make(map[string]struct{})
	for len(remaining) > 0 {
		key, comment, options, rest, err := ssh.ParseAuthorizedKey(remaining)
		if err != nil {
			return nil, fmt.Errorf("解析 Worker SSH 授权公钥: %w", err)
		}
		if len(options) > 0 {
			return nil, errors.New("worker SSH 授权公钥不允许 command 等选项")
		}
		encoded := string(key.Marshal())
		if _, exists := seen[encoded]; exists {
			return nil, errors.New("worker SSH 授权公钥重复")
		}
		seen[encoded] = struct{}{}
		id := strings.TrimSpace(comment)
		if id == "" {
			id = ssh.FingerprintSHA256(key)
		}
		clients = append(clients, AuthorizedClient{ID: id, PublicKey: key})
		remaining = bytes.TrimSpace(rest)
	}
	if len(clients) == 0 {
		return nil, errors.New("worker SSH 至少需要一个授权公钥")
	}
	return clients, nil
}
