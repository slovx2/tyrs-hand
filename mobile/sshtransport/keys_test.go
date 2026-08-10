package sshtransport

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"encoding/pem"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestGenerateAndInspectEd25519Key(t *testing.T) {
	encoded, err := GenerateEd25519Key()
	require.NoError(t, err)
	var generated keyDescription
	require.NoError(t, json.Unmarshal([]byte(encoded), &generated))
	require.Contains(t, generated.PrivateKey, "OPENSSH PRIVATE KEY")
	require.Contains(t, generated.PublicKey, "ssh-ed25519")
	require.Contains(t, generated.Fingerprint, "SHA256:")

	inspected, err := InspectPrivateKey(generated.PrivateKey, "")
	require.NoError(t, err)
	var description keyDescription
	require.NoError(t, json.Unmarshal([]byte(inspected), &description))
	require.Equal(t, generated.PublicKey, description.PublicKey)
	require.Equal(t, generated.Fingerprint, description.Fingerprint)
}

func TestInspectEncryptedEd25519AndRSAKeys(t *testing.T) {
	_, edPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	edBlock, err := ssh.MarshalPrivateKeyWithPassphrase(edPrivate, "test", []byte("secret"))
	require.NoError(t, err)
	edEncoded := string(pem.EncodeToMemory(edBlock))
	_, err = InspectPrivateKey(edEncoded, "")
	require.EqualError(t, err, "私钥已加密，需要口令")
	_, err = InspectPrivateKey(edEncoded, "wrong")
	require.EqualError(t, err, "OpenSSH 私钥或口令无效")
	_, err = InspectPrivateKey(edEncoded, "secret")
	require.NoError(t, err)

	rsaPrivate, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	rsaBlock, err := ssh.MarshalPrivateKey(rsaPrivate, "test-rsa")
	require.NoError(t, err)
	rsaDescription, err := InspectPrivateKey(string(pem.EncodeToMemory(rsaBlock)), "")
	require.NoError(t, err)
	require.Contains(t, rsaDescription, "ssh-rsa")

	ecdsaPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ecdsaBlock, err := ssh.MarshalPrivateKey(ecdsaPrivate, "test-ecdsa")
	require.NoError(t, err)
	ecdsaDescription, err := InspectPrivateKey(string(pem.EncodeToMemory(ecdsaBlock)), "")
	require.NoError(t, err)
	require.Contains(t, ecdsaDescription, "ecdsa-sha2-nistp256")
}
