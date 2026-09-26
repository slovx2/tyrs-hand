//go:build integration

package bootstrap

import (
	"errors"
	"net"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func requireControlNetworkIsolation(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "linux" {
		interfaces, err := net.Interfaces()
		require.NoError(t, err)
		for _, iface := range interfaces {
			require.True(t, iface.Flags&net.FlagLoopback != 0, "真实验收必须运行在仅回环的独立 network namespace：%s", iface.Name)
		}
	}
	// 使用文档保留地址，不访问公网模型；只有明确的 OS 拒绝可证明隔离，超时不能代替。
	connection, err := net.DialTimeout("tcp", "192.0.2.1:443", 250*time.Millisecond)
	if connection != nil {
		_ = connection.Close()
	}
	require.Error(t, err)
	require.True(t, errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENETUNREACH),
		"缺少明确的网络沙箱拒绝；不得把无外层隔离的功能测试作为验收：%v", err)
}
