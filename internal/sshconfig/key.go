package sshconfig

import (
	"errors"
	"strings"

	"golang.org/x/crypto/ssh"
)

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
