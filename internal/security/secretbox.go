package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

type SecretBox struct {
	aead cipher.AEAD
}

func NewSecretBox(key []byte) (*SecretBox, error) {
	if len(key) != 32 {
		return nil, errors.New("主密钥必须是 32 字节")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &SecretBox{aead: aead}, nil
}

func (b *SecretBox) Encrypt(plaintext []byte, associatedData string) (nonce, ciphertext []byte, err error) {
	nonce = make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("生成 nonce: %w", err)
	}
	ciphertext = b.aead.Seal(nil, nonce, plaintext, []byte(associatedData))
	return nonce, ciphertext, nil
}

func (b *SecretBox) Decrypt(nonce, ciphertext []byte, associatedData string) ([]byte, error) {
	if len(nonce) != b.aead.NonceSize() {
		return nil, errors.New("密钥解密失败")
	}
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, []byte(associatedData))
	if err != nil {
		return nil, errors.New("密钥解密失败")
	}
	return plaintext, nil
}

func RandomToken(bytesCount int) (string, error) {
	data := make([]byte, bytesCount)
	if _, err := io.ReadFull(rand.Reader, data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
