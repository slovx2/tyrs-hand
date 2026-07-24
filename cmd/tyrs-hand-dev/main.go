package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var exactVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) < 2 || arguments[0] != "codex" {
		return errors.New("用法：tyrs-hand-dev codex <install|status|rollback|reset> [version]")
	}
	root, marker, err := userPaths()
	if err != nil {
		return err
	}
	switch arguments[1] {
	case "install":
		if len(arguments) != 3 || !exactVersion.MatchString(arguments[2]) {
			return errors.New("codex install 必须提供精确版本，例如 0.145.0")
		}
		return installCodex(root, marker, arguments[2])
	case "status":
		if len(arguments) != 2 {
			return errors.New("codex status 不接受额外参数")
		}
		return printStatus(root)
	case "rollback":
		if len(arguments) != 2 {
			return errors.New("codex rollback 不接受额外参数")
		}
		return rollbackCodex(root, marker)
	case "reset":
		if len(arguments) != 2 {
			return errors.New("codex reset 不接受额外参数")
		}
		return resetCodex(root, marker)
	default:
		return fmt.Errorf("未知 Codex 操作 %q", arguments[1])
	}
}

func userPaths() (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	root := filepath.Join(home, ".local", "share", "tyrs-hand", "codex")
	marker := filepath.Join(home, ".local", "state", "tyrs-hand", "codex-restart-required")
	return root, marker, nil
}

func installCodex(root, marker, version string) error {
	versions := filepath.Join(root, "versions")
	if err := os.MkdirAll(versions, 0o700); err != nil {
		return err
	}
	destination := filepath.Join(versions, version)
	if _, err := os.Stat(destination); errors.Is(err, os.ErrNotExist) {
		temporary := filepath.Join(versions, ".install-"+version+"-"+fmt.Sprint(time.Now().UnixNano()))
		defer func() { _ = os.RemoveAll(temporary) }()
		command := exec.Command("npm", "install", "--prefix", temporary, "--omit=dev",
			"@openai/codex@"+version)
		command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			return fmt.Errorf("安装 Codex %s: %w", version, err)
		}
		native, err := findNativeCodex(temporary)
		if err != nil {
			return err
		}
		if output, err := exec.Command(native, "--version").CombinedOutput(); err != nil {
			return fmt.Errorf("验证 Codex %s: %w：%s", version, err, strings.TrimSpace(string(output)))
		}
		binDir := filepath.Join(temporary, "bin")
		if err := os.MkdirAll(binDir, 0o700); err != nil {
			return err
		}
		relative, err := filepath.Rel(binDir, native)
		if err != nil {
			return err
		}
		if err := os.Symlink(relative, filepath.Join(binDir, "codex")); err != nil {
			return err
		}
		if err := os.Rename(temporary, destination); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := switchVersion(root, destination); err != nil {
		return err
	}
	if err := writeMarker(marker); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stdout, "Codex %s 已安装，将在环境空闲后启用。\n", version)
	return nil
}

func findNativeCodex(root string) (string, error) {
	var result string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if result == "" && !entry.IsDir() && entry.Name() == "codex" &&
			strings.Contains(filepath.ToSlash(path), "/vendor/") &&
			strings.Contains(filepath.ToSlash(path), "/bin/") {
			result = path
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if result == "" {
		return "", errors.New("安装包中没有找到 Codex 原生程序")
	}
	return result, nil
}

func switchVersion(root, destination string) error {
	current := filepath.Join(root, "current")
	if existing, err := os.Readlink(current); err == nil {
		if err := replaceSymlink(filepath.Join(root, "previous"), existing); err != nil {
			return err
		}
	}
	return replaceSymlink(current, destination)
}

func replaceSymlink(path, target string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".tmp-" + fmt.Sprint(time.Now().UnixNano())
	if err := os.Symlink(target, temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func rollbackCodex(root, marker string) error {
	current, currentErr := os.Readlink(filepath.Join(root, "current"))
	previous, previousErr := os.Readlink(filepath.Join(root, "previous"))
	if currentErr != nil || previousErr != nil {
		return errors.New("没有可回退的 Codex 用户版本")
	}
	if err := replaceSymlink(filepath.Join(root, "current"), previous); err != nil {
		return err
	}
	if err := replaceSymlink(filepath.Join(root, "previous"), current); err != nil {
		return err
	}
	return writeMarker(marker)
}

func resetCodex(root, marker string) error {
	current := filepath.Join(root, "current")
	if existing, err := os.Readlink(current); err == nil {
		if err := replaceSymlink(filepath.Join(root, "previous"), existing); err != nil {
			return err
		}
	}
	if err := os.Remove(current); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeMarker(marker)
}

func printStatus(root string) error {
	current, err := os.Readlink(filepath.Join(root, "current"))
	if errors.Is(err, os.ErrNotExist) {
		_, _ = fmt.Fprintln(os.Stdout, "bundled")
		return nil
	}
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(os.Stdout, filepath.Base(current))
	return nil
}

func writeMarker(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600)
}
