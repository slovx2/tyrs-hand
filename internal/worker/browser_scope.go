package worker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// LoadBrowserScope 为宿主浏览器持久化独立身份，不受 Control 绑定变化影响。
func LoadBrowserScope(root string) (uuid.UUID, error) {
	path := filepath.Join(root, "browser-scope")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return uuid.Nil, err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		temporary, err := os.CreateTemp(root, ".browser-scope-*")
		if err != nil {
			return uuid.Nil, err
		}
		defer func() { _ = os.Remove(temporary.Name()) }()
		_, writeErr := temporary.WriteString(uuid.NewString() + "\n")
		syncErr := temporary.Sync()
		closeErr := temporary.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return uuid.Nil, err
		}
		if err := os.Link(temporary.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
			return uuid.Nil, err
		}
		directory, err := os.Open(root)
		if err != nil {
			return uuid.Nil, err
		}
		syncErr = directory.Sync()
		closeErr = directory.Close()
		if err := errors.Join(syncErr, closeErr); err != nil {
			return uuid.Nil, err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return uuid.Nil, err
	}
	if !info.Mode().IsRegular() {
		return uuid.Nil, errors.New("浏览器身份必须是普通文件")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return uuid.Nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return uuid.Nil, err
	}
	id, err := uuid.Parse(strings.TrimSpace(string(data)))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, errors.New("浏览器身份文件无效")
	}
	return id, nil
}
