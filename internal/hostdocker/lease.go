package hostdocker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

func withLeaseLock(root, runID string, action func() error) error {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return fmt.Errorf("创建 Docker Lease 目录: %w", err)
	}
	lock, err := os.OpenFile(lockPath(root, runID), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("打开 Docker Lease 锁: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("锁定 Docker Lease: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	return action()
}

func readLease(root, runID string) (runLease, error) {
	data, err := os.ReadFile(leasePath(root, runID))
	if err != nil {
		return runLease{}, err
	}
	var lease runLease
	if err := json.Unmarshal(data, &lease); err != nil {
		return runLease{}, fmt.Errorf("解析 Docker Run Lease: %w", err)
	}
	return lease, nil
}

func writeLease(root string, lease runLease) error {
	data, err := lease.encode()
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(root, ".lease-*.tmp")
	if err != nil {
		return fmt.Errorf("创建 Docker Lease 临时文件: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("写入 Docker Run Lease: %w", err)
	}
	if err := os.Rename(temporaryPath, leasePath(root, lease.Scope.RunID)); err != nil {
		return fmt.Errorf("发布 Docker Run Lease: %w", err)
	}
	return nil
}

func updateLease(root, runID string, update func(*runLease) error) error {
	return withLeaseLock(root, runID, func() error {
		lease, err := readLease(root, runID)
		if err != nil {
			return err
		}
		if err := update(&lease); err != nil {
			return err
		}
		return writeLease(root, lease)
	})
}

func recordContainers(root, runID string, containers []string) error {
	_, err := reserveContainers(root, runID, containers)
	return err
}

func reserveContainers(root, runID string, containers []string) ([]string, error) {
	if len(containers) == 0 {
		return nil, nil
	}
	var added []string
	err := updateLease(root, runID, func(lease *runLease) error {
		if lease.Ended {
			return errors.New("docker Run Lease 已结束")
		}
		for _, container := range containers {
			container = filepath.Base(container)
			if container != "." && container != "" && !slices.Contains(lease.Containers, container) {
				lease.Containers = append(lease.Containers, container)
				added = append(added, container)
			}
		}
		return nil
	})
	return added, err
}

func releaseContainers(root, runID string, containers []string) error {
	if len(containers) == 0 {
		return nil
	}
	return updateLease(root, runID, func(lease *runLease) error {
		lease.Containers = slices.DeleteFunc(lease.Containers, func(container string) bool {
			return slices.Contains(containers, container)
		})
		return nil
	})
}
