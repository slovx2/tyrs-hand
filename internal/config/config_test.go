package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	valid := Config{
		Environment: "development", HTTPAddr: ":8080", DatabaseURL: "postgres://db", RedisURL: "redis://cache",
		CodexBin: "codex", WorkerID: "worker", LeaseDuration: 90 * time.Second, HeartbeatInterval: 20 * time.Second,
		RepoCacheMaxBytes: 1024,
	}
	require.NoError(t, valid.Validate())
	invalid := valid
	invalid.DatabaseURL = ""
	require.Error(t, invalid.Validate())
	invalid = valid
	invalid.LeaseDuration = 30 * time.Second
	require.Error(t, invalid.Validate())
	production := valid
	production.Environment = "production"
	require.Error(t, production.Validate())
	production.MasterKey = make([]byte, 32)
	production.CookieSecure = true
	require.NoError(t, production.Validate())
}

func TestReadSecretAndLoadMasterKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master-key")
	encoded := base64.StdEncoding.EncodeToString(make([]byte, 32))
	require.NoError(t, os.WriteFile(path, []byte(encoded+"\n"), 0o600))
	value, err := readSecret("", path)
	require.NoError(t, err)
	require.Equal(t, encoded, value)
	_, err = readSecret(encoded, path)
	require.Error(t, err)

	t.Setenv("TYRS_HAND_MASTER_KEY", encoded)
	t.Setenv("TYRS_HAND_DATABASE_URL", "postgres://db")
	t.Setenv("TYRS_HAND_REDIS_URL", "redis://cache")
	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.MasterKey, 32)
	require.False(t, strings.HasSuffix(cfg.PublicURL, "/"))
}
