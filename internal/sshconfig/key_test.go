package sshconfig

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestParsePrivateKey(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(privateKey, "test")
	require.NoError(t, err)

	publicKey, fingerprint, err := parsePrivateKey(string(ssh.MarshalAuthorizedKey(blockKey(t, block))), "")
	require.Error(t, err)
	require.Empty(t, publicKey)
	require.Empty(t, fingerprint)

	publicKey, fingerprint, err = parsePrivateKey(string(pemBytes(block)), "")
	require.NoError(t, err)
	require.Contains(t, publicKey, "ssh-rsa")
	require.Contains(t, fingerprint, "SHA256:")
}

func TestParseEncryptedPrivateKey(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(privateKey, "test", []byte("secret"))
	require.NoError(t, err)

	_, _, err = parsePrivateKey(string(pemBytes(block)), "wrong")
	require.Error(t, err)
	_, fingerprint, err := parsePrivateKey(string(pemBytes(block)), "secret")
	require.NoError(t, err)
	require.Contains(t, fingerprint, "SHA256:")
}

func TestNormalizeHostRejectsConfigInjection(t *testing.T) {
	_, err := normalizeHost(HostInput{Alias: "server", Hostname: "host\nProxyJump evil",
		Username: "ubuntu", Port: 22})
	require.ErrorContains(t, err, "空白字符")
}

func TestPrivateKeyAndCredentialValidationBranches(t *testing.T) {
	_, _, err := parsePrivateKey("  ", "")
	require.ErrorContains(t, err, "不能为空")
	_, _, err = parsePrivateKey("not-a-private-key", "")
	require.ErrorContains(t, err, "无效")

	_, _, _, err = normalizeCredential(CredentialInput{Name: ""}, true)
	require.ErrorContains(t, err, "名称")
	_, _, _, err = normalizeCredential(CredentialInput{Name: "credential"}, true)
	require.ErrorContains(t, err, "不能为空")
	input, publicKey, fingerprint, err := normalizeCredential(CredentialInput{
		Name: " credential ", PrivateKey: "",
	}, false)
	require.NoError(t, err)
	require.Equal(t, "credential", input.Name)
	require.Empty(t, publicKey)
	require.Empty(t, fingerprint)
}

func TestNormalizeHostDefaultsDeduplicatesAndRejectsInvalidFields(t *testing.T) {
	nodeA := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	nodeB := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	input, err := normalizeHost(HostInput{Alias: " server_1 ", Hostname: "example.com",
		Username: "ubuntu", ExecutionNodeIDs: []uuid.UUID{nodeB, nodeA, nodeB, uuid.Nil}})
	require.NoError(t, err)
	require.Equal(t, "server_1", input.Alias)
	require.Equal(t, 22, input.Port)
	require.Equal(t, []uuid.UUID{nodeA, nodeB}, input.ExecutionNodeIDs)

	for _, test := range []struct {
		name  string
		input HostInput
		error string
	}{
		{name: "invalid alias", input: HostInput{Alias: "bad alias", Hostname: "host", Username: "user"}, error: "别名"},
		{name: "empty hostname", input: HostInput{Alias: "server", Username: "user"}, error: "不能为空"},
		{name: "username injection", input: HostInput{Alias: "server", Hostname: "host", Username: "root\nProxyCommand evil"}, error: "空白字符"},
		{name: "low port", input: HostInput{Alias: "server", Hostname: "host", Username: "user", Port: -1}, error: "端口"},
		{name: "high port", input: HostInput{Alias: "server", Hostname: "host", Username: "user", Port: 65536}, error: "端口"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeHost(test.input)
			require.ErrorContains(t, err, test.error)
		})
	}
}

func TestOrderHostImportsPlacesProxyFirst(t *testing.T) {
	hosts := []HostInput{{Alias: "target"}, {Alias: "jump"}, {Alias: "direct"}}
	order, indexes, err := orderHostImports(hosts, []string{"jump", "", "external"})
	require.NoError(t, err)
	require.Equal(t, map[string]int{"target": 0, "jump": 1, "direct": 2}, indexes)
	require.Less(t, indexOf(order, 1), indexOf(order, 0))

	_, _, err = orderHostImports([]HostInput{{Alias: "duplicate"}, {Alias: "duplicate"}},
		[]string{"", ""})
	require.ErrorContains(t, err, "重复")
	_, _, err = orderHostImports([]HostInput{{Alias: "one"}, {Alias: "two"}},
		[]string{"two", "one"})
	require.ErrorContains(t, err, "循环")
}

func indexOf(values []int, target int) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
}

func pemBytes(block *pem.Block) []byte {
	return pem.EncodeToMemory(block)
}

func blockKey(t *testing.T, block *pem.Block) ssh.PublicKey {
	t.Helper()
	key, err := ssh.ParseRawPrivateKey(pemBytes(block))
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromKey(key)
	require.NoError(t, err)
	return signer.PublicKey()
}
