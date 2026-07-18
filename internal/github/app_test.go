package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

func TestAppClientInstallationTokenCacheAndPermission(t *testing.T) {
	privateKey := rsaKey(t)
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/app/installations/42/access_tokens":
			tokenCalls.Add(1)
			require.Equal(t, http.MethodPost, request.Method)
			authorization := request.Header.Get("Authorization")
			require.True(t, strings.HasPrefix(authorization, "Bearer "))
			claims := &jwt.RegisteredClaims{}
			_, err := jwt.ParseWithClaims(strings.TrimPrefix(authorization, "Bearer "), claims, func(*jwt.Token) (any, error) { return &privateKey.PublicKey, nil })
			require.NoError(t, err)
			require.Equal(t, "123", claims.Issuer)
			_ = json.NewEncoder(response).Encode(map[string]any{"token": "installation-token", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		case "/repos/owner/repo/collaborators/alice/permission":
			require.Equal(t, "Bearer installation-token", request.Header.Get("Authorization"))
			_ = json.NewEncoder(response).Encode(map[string]any{"permission": "write", "user": map[string]any{"login": "alice"}})
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	client, err := NewAppClient(123, pemKey(privateKey), server.URL+"/")
	require.NoError(t, err)
	first, err := client.InstallationToken(context.Background(), 42)
	require.NoError(t, err)
	second, err := client.InstallationToken(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.EqualValues(t, 1, tokenCalls.Load())
	permission, err := client.Permission(context.Background(), 42, "owner", "repo", "alice")
	require.NoError(t, err)
	require.Equal(t, "write", permission)
}

func TestNewAppClientRejectsInvalidConfiguration(t *testing.T) {
	_, err := NewAppClient(1, []byte("not a pem"), "")
	require.Error(t, err)
	_, err = NewAppClient(1, pemKey(rsaKey(t)), "://invalid")
	require.Error(t, err)
}

func rsaKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key
}

func pemKey(key *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}
