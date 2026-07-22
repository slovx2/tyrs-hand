package sshconfig

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

func ParseAuthorizedPublicKey(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", errors.New("SSH 公钥不能为空")
	}
	publicKey, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(value))
	_ = comment
	if err != nil {
		return "", "", errors.New("SSH 公钥格式无效")
	}
	if len(options) > 0 {
		return "", "", fmt.Errorf("SSH 公钥不能包含 authorized_keys options")
	}
	if strings.TrimSpace(string(rest)) != "" {
		return "", "", errors.New("每个环境只能配置一个 SSH 公钥")
	}
	normalized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(publicKey)))
	return normalized, ssh.FingerprintSHA256(publicKey), nil
}

func parsePrivateKey(privateKey, passphrase string) (string, string, error) {
	privateKey = strings.TrimSpace(privateKey)
	if privateKey == "" {
		return "", "", errors.New("私钥不能为空")
	}
	var (
		raw any
		err error
	)
	if passphrase == "" {
		raw, err = ssh.ParseRawPrivateKey([]byte(privateKey))
	} else {
		raw, err = ssh.ParseRawPrivateKeyWithPassphrase([]byte(privateKey), []byte(passphrase))
	}
	if err != nil {
		return "", "", errors.New("私钥或口令无效")
	}
	signer, err := ssh.NewSignerFromKey(raw)
	if err != nil {
		return "", "", errors.New("不支持该私钥格式")
	}
	publicKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	return publicKey, ssh.FingerprintSHA256(signer.PublicKey()), nil
}

func enabledValue(value *bool) bool {
	return value == nil || *value
}
