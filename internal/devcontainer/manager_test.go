package devcontainer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestParseIdentityAndDisabledManager(t *testing.T) {
	uid, gid, home, err := parseIdentity("1001:1002:/home/dev")
	require.NoError(t, err)
	require.EqualValues(t, 1001, uid)
	require.EqualValues(t, 1002, gid)
	require.Equal(t, "/home/dev", home)

	for _, invalid := range []string{"", "1001:1002", "user:1002:/home/dev", "1001:group:/home/dev"} {
		_, _, _, err = parseIdentity(invalid)
		require.Error(t, err)
	}

	manager, err := NewManager(config.Config{
		EnableDevelopmentContainers: true, WorkerRole: "github",
	}, nil, zap.NewNop())
	require.NoError(t, err)
	require.False(t, manager.Enabled())
	manager.RunSweeper(context.Background())
	manager.releaseBuildLock(nil)
	manager.restorePreviousContainer("", "")
	require.Equal(t, "error", zapError(errors.New("error")).Key)
	require.Equal(t, "container", zapString("container", "dev").Key)
	_, err = manager.Ensure(context.Background(), uuid.Nil, uuid.Nil, uuid.Nil, "")
	require.ErrorContains(t, err, "未启用")
}

func TestExecRunnerAndAskPass(t *testing.T) {
	runner := execRunner{}
	_, err := runner.Run(context.Background(), nil, "")
	require.ErrorContains(t, err, "不能为空")
	directory := t.TempDir()
	value, err := runner.Run(context.Background(), []string{"TEST_VALUE=ready"}, directory,
		"sh", "-c", "printf '%s:%s' \"$TEST_VALUE\" \"$PWD\"")
	require.NoError(t, err)
	resolvedDirectory, err := filepath.EvalSymlinks(directory)
	require.NoError(t, err)
	require.Equal(t, "ready:"+resolvedDirectory, value)
	_, err = runner.Run(context.Background(), nil, directory, "sh", "-c", "printf failed; exit 9")
	require.ErrorContains(t, err, "failed")

	path, cleanup, err := createAskPass("secret")
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "TYRS_GIT_TOKEN")
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	directory = filepath.Dir(path)
	cleanup()
	require.NoDirExists(t, directory)
}
