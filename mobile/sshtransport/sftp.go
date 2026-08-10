package sshtransport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/pkg/sftp"
)

type directoryEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
}

type uploadedAttachment struct {
	RemotePath string `json:"remotePath"`
	SHA256     string `json:"sha256"`
}

func ListDirectory(host string, port int, user, privateKey, passphrase,
	expectedHostFingerprint, remotePath string,
) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := connectSSH(ctx, sshOptions{host: host, port: port, user: user,
		privateKey: privateKey, passphrase: passphrase,
		expectedHostFingerprint: expectedHostFingerprint})
	if err != nil {
		return "", err
	}
	defer func() { _ = client.Close() }()
	filesystem, err := sftp.NewClient(client)
	if err != nil {
		return "", err
	}
	defer func() { _ = filesystem.Close() }()
	remotePath = path.Clean(strings.TrimSpace(remotePath))
	if !path.IsAbs(remotePath) {
		return "", errors.New("SFTP 浏览路径必须是绝对路径")
	}
	entries, err := filesystem.ReadDir(remotePath)
	if err != nil {
		return "", err
	}
	result := make([]directoryEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == "." || entry.Name() == ".." {
			continue
		}
		result = append(result, directoryEntry{Name: entry.Name(),
			Path: path.Join(remotePath, entry.Name()), Directory: entry.IsDir()})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Directory != result[j].Directory {
			return result[i].Directory
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return marshalJSON(result)
}

func UploadAttachment(host string, port int, user, privateKey, passphrase,
	expectedHostFingerprint, localPath, _, _ string,
) (string, error) {
	digest, size, err := localFileDigest(localPath)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client, err := connectSSH(ctx, sshOptions{host: host, port: port, user: user,
		privateKey: privateKey, passphrase: passphrase,
		expectedHostFingerprint: expectedHostFingerprint})
	if err != nil {
		return "", err
	}
	defer func() { _ = client.Close() }()
	filesystem, err := sftp.NewClient(client)
	if err != nil {
		return "", err
	}
	defer func() { _ = filesystem.Close() }()
	home, err := filesystem.Getwd()
	if err != nil || !path.IsAbs(home) {
		return "", errors.New("无法读取远端用户 Home")
	}
	directory := path.Join(home, ".cache", "tyrs-hand", "attachments")
	if err = filesystem.MkdirAll(directory); err != nil {
		return "", err
	}
	if err = filesystem.Chmod(directory, 0o700); err != nil {
		return "", err
	}
	remotePath := path.Join(directory, digest)
	if info, statErr := filesystem.Stat(remotePath); statErr == nil &&
		info.Mode().IsRegular() && info.Size() == size {
		return marshalJSON(uploadedAttachment{RemotePath: remotePath, SHA256: digest})
	}
	token, err := randomLoopbackToken()
	if err != nil {
		return "", err
	}
	temporaryPath := path.Join(directory, "."+digest+"."+token+".tmp")
	source, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = source.Close() }()
	destination, err := filesystem.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = destination.Close()
		_ = filesystem.Remove(temporaryPath)
	}()
	if err = destination.Chmod(0o600); err != nil {
		return "", err
	}
	written, err := io.Copy(destination, io.LimitReader(source, (25<<20)+1))
	if err != nil || written != size {
		return "", errors.New("SFTP 附件上传不完整")
	}
	if err = destination.Close(); err != nil {
		return "", err
	}
	if err = filesystem.PosixRename(temporaryPath, remotePath); err != nil {
		return "", err
	}
	return marshalJSON(uploadedAttachment{RemotePath: remotePath, SHA256: digest})
}

func localFileDigest(localPath string) (string, int64, error) {
	file, err := os.Open(localPath)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, (25<<20)+1))
	if err != nil {
		return "", 0, err
	}
	if written <= 0 || written > 25<<20 {
		return "", 0, errors.New("附件为空或超过 25 MiB")
	}
	return hex.EncodeToString(hash.Sum(nil)), written, nil
}
