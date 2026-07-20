package hostdocker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBaseComposeDoesNotExposeDockerSocket(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	base, err := os.ReadFile(filepath.Join(root, "compose.yaml"))
	require.NoError(t, err)
	require.NotContains(t, string(base), "/var/run/docker.sock")

	override, err := os.ReadFile(filepath.Join(root, "compose.host-docker.example.yaml"))
	require.NoError(t, err)
	text := string(override)
	require.Contains(t, text, "Beta。有安全风险，请确保所有用户可信再开启。")
	require.Contains(t, text, "/var/run/docker.sock")
	require.Contains(t, text, "TYRS_HAND_DOCKER_GID:?")
	require.Contains(t, text, "TYRS_HAND_ENABLE_HOST_DOCKER: \"true\"")
	require.Contains(t, text, "tyrs-hand-agent-runtime")
}

func TestProductionComposeEnablesHostDocker(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	production, err := os.ReadFile(filepath.Join(root, "compose.production.yaml"))
	require.NoError(t, err)
	text := string(production)
	require.Contains(t, text, "/var/run/docker.sock")
	require.Contains(t, text, "TYRS_HAND_DOCKER_GID:?")
	require.Contains(t, text, "TYRS_HAND_ENABLE_HOST_DOCKER: \"true\"")
	require.Contains(t, text, "tyrs-hand-agent-runtime")
}

func TestWorkerImageKeepsNonRootUserAndSeparateDockerCLI(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	data, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	require.NoError(t, err)
	text := string(data)
	require.GreaterOrEqual(t, strings.Count(text, "USER 10001:10001"), 2)
	require.Contains(t, text, "dockerCli")
	require.Contains(t, text, RealDockerBinary)
	require.NotContains(t, text, "docker compose")
}
