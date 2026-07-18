package security

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecretBoxRoundTrip(t *testing.T) {
	box, err := NewSecretBox(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	nonce, ciphertext, err := box.Encrypt([]byte("secret"), "github.private-key")
	require.NoError(t, err)

	plaintext, err := box.Decrypt(nonce, ciphertext, "github.private-key")
	require.NoError(t, err)
	require.Equal(t, []byte("secret"), plaintext)
	_, err = box.Decrypt(nonce, ciphertext, "wrong")
	require.Error(t, err)
}

func TestPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	require.NoError(t, err)
	require.True(t, VerifyPassword(hash, "correct horse battery staple"))
	require.False(t, VerifyPassword(hash, "wrong password"))
	_, err = HashPassword("short")
	require.Error(t, err)
}

func TestSecurityValidationAndTokens(t *testing.T) {
	_, err := NewSecretBox([]byte("short"))
	require.Error(t, err)
	box, err := NewSecretBox(bytes.Repeat([]byte{1}, 32))
	require.NoError(t, err)
	_, err = box.Decrypt([]byte("bad"), []byte("ciphertext"), "context")
	require.Error(t, err)
	token, err := RandomToken(32)
	require.NoError(t, err)
	require.Len(t, token, 43)
	require.Len(t, Digest("value"), 64)

	for _, encoded := range []string{
		"", "$argon2i$v=19$m=1,t=1,p=1$a$b", "$argon2id$v=18$m=1,t=1,p=1$a$b",
		"$argon2id$v=19$invalid$a$b", "$argon2id$v=19$m=1,t=1,p=1$!$b",
		"$argon2id$v=19$m=1,t=1,p=1$YQ$!", "$argon2id$v=19$m=1,t=1,p=1$YQ$",
	} {
		require.False(t, VerifyPassword(encoded, "password"), encoded)
	}
}
