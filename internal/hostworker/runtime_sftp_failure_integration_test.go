//go:build integration

package hostworker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/slovx2/tyrs-hand/mobile/sshtransport"
	"github.com/stretchr/testify/require"
)

// 只转发真实加密 SSH 字节，并在传输已经开始后断开 TCP。
// 不伪造 SFTP 服务或返回值，不把握手失败当作中途失败。
func sftpCutProxy(t *testing.T, target string, upload bool) (*net.TCPAddr, <-chan int64) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	transferred := make(chan int64, 1)
	go func() {
		defer close(transferred)
		client, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = client.Close() }()
		server, dialErr := net.DialTimeout("tcp", target, 5*time.Second)
		if dialErr != nil {
			return
		}
		defer func() { _ = server.Close() }()
		_ = client.SetDeadline(time.Now().Add(15 * time.Second))
		_ = server.SetDeadline(time.Now().Add(15 * time.Second))
		source, destination := server, client
		if upload {
			source, destination = client, server
		}
		finished := make(chan struct{})
		go func() {
			_, _ = io.Copy(source, destination)
			close(finished)
		}()
		written, _ := io.CopyN(destination, source, 64<<10)
		transferred <- written
		_ = client.Close()
		_ = server.Close()
		<-finished
	}()
	return listener.Addr().(*net.TCPAddr), transferred
}

func verifyRuntimeSFTPFailures(t *testing.T, entry *RuntimeEntry, privateKey, root string) {
	t.Helper()
	address := entry.SSH.Addr().(*net.TCPAddr)
	host, port, fingerprint := address.IP.String(), address.Port, entry.SSH.HostKeyFingerprint()
	content := bytes.Repeat([]byte("sftp-interrupted-native-transport\x00\xff"), 8192)
	local := filepath.Join(root, "interrupted-upload.dat")
	require.NoError(t, os.WriteFile(local, content, 0o600))
	digest := sha256.Sum256(content)
	remote := filepath.Join(entry.Runtime.Home(), ".cache", "tyrs-hand", "attachments", hex.EncodeToString(digest[:]))
	proxy, uploadedBytes := sftpCutProxy(t, entry.SSH.Addr().String(), true)
	_, err := sshtransport.UploadAttachment(proxy.IP.String(), proxy.Port, "mobile", privateKey, "", fingerprint, local, "interrupted.dat", "application/octet-stream")
	require.Error(t, err, "SSH 中途断开不能误报附件上传成功")
	require.Equal(t, int64(64<<10), <-uploadedBytes, "必须在实际传输后才断开")
	_, err = os.Stat(remote)
	require.True(t, os.IsNotExist(err), "部分上传不能发布为内容寻址缓存")
	// 同一附件重试必须重新完成，不得消费上次未完成的临时文件。
	result, err := sshtransport.UploadAttachment(host, port, "mobile", privateKey, "", fingerprint, local, "retry.dat", "application/octet-stream")
	require.NoError(t, err)
	var uploaded struct{ RemotePath string }
	require.NoError(t, json.Unmarshal([]byte(result), &uploaded))
	require.Equal(t, remote, uploaded.RemotePath)
	actual, err := os.ReadFile(remote)
	require.NoError(t, err)
	require.Equal(t, content, actual)
	proxy, downloadedBytes := sftpCutProxy(t, entry.SSH.Addr().String(), false)
	destination := filepath.Join(root, string(entry.Runtime.Info().Engine)+"-interrupted-download.dat")
	_, err = sshtransport.DownloadFile(proxy.IP.String(), proxy.Port, "mobile", privateKey, "", fingerprint, remote, destination)
	require.Error(t, err, "SSH 中途断开不能误报图片下载成功")
	require.Equal(t, int64(64<<10), <-downloadedBytes)
	_, err = os.Stat(destination)
	require.True(t, os.IsNotExist(err), "部分下载不能发布为客户端缓存")
	leftovers, err := filepath.Glob(destination + ".*.tmp")
	require.NoError(t, err)
	require.Empty(t, leftovers, "中断下载必须清理本地临时文件")
	_, err = sshtransport.DownloadFile(host, port, "mobile", privateKey, "", fingerprint, remote, destination)
	require.NoError(t, err)
	actual, err = os.ReadFile(destination)
	require.NoError(t, err)
	require.Equal(t, content, actual, "中断后重新下载必须校验完整字节")
	oversized := filepath.Join(root, "over-limit.dat")
	file, err := os.Create(oversized)
	require.NoError(t, err)
	require.NoError(t, file.Truncate((25<<20)+1))
	require.NoError(t, file.Close())
	_, err = sshtransport.UploadAttachment(host, port, "mobile", privateKey, "", fingerprint, oversized, "over.dat", "application/octet-stream")
	require.ErrorContains(t, err, "超过 25 MiB")
	_, err = sshtransport.DownloadFile(host, port, "mobile", privateKey, "", fingerprint, oversized, destination+"-over-limit")
	require.ErrorContains(t, err, "超过 25 MiB")
	_, err = os.Stat(destination + "-over-limit")
	require.True(t, os.IsNotExist(err))
}
