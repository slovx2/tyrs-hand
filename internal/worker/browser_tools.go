package worker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/devcontainer"
	"github.com/slovx2/tyrs-hand/internal/ports"
)

const (
	browserFileLimit     = 25 * 1024 * 1024
	browserToolNamespace = "host_browser"
)

type stagedBrowserFile struct {
	HostPath  string    `json:"hostPath"`
	SHA256    string    `json:"sha256"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func browserToolSpec() ports.DynamicToolSpec {
	return ports.DynamicToolSpec{
		Type: "namespace", Name: browserToolNamespace,
		Description: "Expose local development servers and exchange files with the managed host Chrome.",
		Tools: []ports.DynamicToolSpec{
			{Type: "function", Name: "resolve_local_url",
				Description: "Resolve a service listening on 0.0.0.0 to a URL reachable by host Chrome.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"port":{"type":"integer","minimum":1,"maximum":65535}},"required":["port"],"additionalProperties":false}`)},
			{Type: "function", Name: "stage_file",
				Description: "Copy a regular file from the current workspace into the host browser exchange directory.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"source":{"type":"string","minLength":1}},"required":["source"],"additionalProperties":false}`)},
			{Type: "function", Name: "import_download",
				Description: "Copy a browser download from the exchange directory into the current workspace.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"source":{"type":"string","minLength":1},"destination":{"type":"string","minLength":1}},"required":["source","destination"],"additionalProperties":false}`)},
		},
	}
}

func withBrowserTools(cfg config.Config, specs ...ports.DynamicToolSpec) []ports.DynamicToolSpec {
	if cfg.BrowserMCPURL != "" {
		specs = append(specs, browserToolSpec())
	}
	return specs
}

func executeBrowserTool(ctx context.Context, cfg config.Config, taskID, workspace string,
	runtime *devcontainer.Runtime, development *devcontainer.Manager,
	request codex.ToolCallRequest,
) (codex.ToolCallResult, error) {
	if cfg.BrowserMCPURL == "" {
		return codex.ToolCallResult{}, errors.New("宿主浏览器能力未配置")
	}
	switch request.Tool {
	case "resolve_local_url":
		var arguments struct {
			Port int `json:"port"`
		}
		if err := json.Unmarshal(request.Arguments, &arguments); err != nil {
			return codex.ToolCallResult{}, err
		}
		address, err := workerIPv4()
		if runtime != nil {
			address, err = development.ContainerIP(ctx, *runtime)
		}
		if err != nil {
			return codex.ToolCallResult{}, err
		}
		if err := probePort(ctx, address, arguments.Port); err != nil {
			return codex.ToolCallResult{}, err
		}
		value := (&url.URL{Scheme: "http", Host: net.JoinHostPort(address,
			fmt.Sprintf("%d", arguments.Port))}).String()
		return browserJSONResult(map[string]string{"url": value})
	case "stage_file":
		var arguments struct {
			Source string `json:"source"`
		}
		if err := json.Unmarshal(request.Arguments, &arguments); err != nil {
			return codex.ToolCallResult{}, err
		}
		return stageBrowserFile(ctx, cfg, taskID, workspace, runtime, development, arguments.Source)
	case "import_download":
		var arguments struct {
			Source      string `json:"source"`
			Destination string `json:"destination"`
		}
		if err := json.Unmarshal(request.Arguments, &arguments); err != nil {
			return codex.ToolCallResult{}, err
		}
		return importBrowserDownload(ctx, cfg, workspace, runtime, development,
			arguments.Source, arguments.Destination)
	default:
		return codex.ToolCallResult{}, fmt.Errorf("浏览器文件工具 %s 不存在", request.Tool)
	}
}

func stageBrowserFile(ctx context.Context, cfg config.Config, taskID, workspace string,
	runtime *devcontainer.Runtime, development *devcontainer.Manager, source string,
) (codex.ToolCallResult, error) {
	directory, hostDirectory, err := browserTaskDirectory(cfg, taskID)
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	name := filepath.Base(filepath.Clean(source))
	target := filepath.Join(directory, name)
	if runtime == nil {
		clean, err := secureWorkspaceFile(workspace, source)
		if err != nil {
			return codex.ToolCallResult{}, err
		}
		if err := copyRegularFile(clean, target); err != nil {
			return codex.ToolCallResult{}, err
		}
	} else {
		if !filepath.IsAbs(source) {
			source = filepath.Join(runtime.Workspace, source)
		}
		if err := development.ExportWorkspaceFile(ctx, *runtime, source, target); err != nil {
			return codex.ToolCallResult{}, err
		}
	}
	info, err := os.Stat(target)
	if err != nil || info.Size() > browserFileLimit {
		_ = os.Remove(target)
		return codex.ToolCallResult{}, errors.New("文件大小超过 25 MiB")
	}
	digest, err := fileSHA256(target)
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	return browserJSONResult(stagedBrowserFile{HostPath: filepath.Join(hostDirectory, name),
		SHA256: digest, ExpiresAt: time.Now().UTC().Add(time.Hour)})
}

func importBrowserDownload(ctx context.Context, cfg config.Config, workspace string,
	runtime *devcontainer.Runtime, development *devcontainer.Manager, source, destination string,
) (codex.ToolCallResult, error) {
	workerSource, err := browserSourcePath(cfg, source)
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	info, err := os.Lstat(workerSource)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return codex.ToolCallResult{}, errors.New("下载源必须是交换目录内的普通文件")
	}
	if info.Size() > browserFileLimit {
		return codex.ToolCallResult{}, errors.New("文件大小超过 25 MiB")
	}
	if runtime != nil {
		if !filepath.IsAbs(destination) {
			destination = filepath.Join(runtime.Workspace, destination)
		}
		err = development.ImportWorkspaceFile(ctx, *runtime, workerSource, destination)
	} else {
		var target string
		target, err = secureWorkspaceDestination(workspace, destination)
		if err == nil {
			err = copyRegularFile(workerSource, target)
		}
	}
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	digest, err := fileSHA256(workerSource)
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	return browserJSONResult(map[string]string{"path": destination, "sha256": digest})
}

func browserTaskDirectory(cfg config.Config, taskID string) (string, string, error) {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return "", "", err
	}
	relative := filepath.Join(taskID, hex.EncodeToString(random))
	directory := filepath.Join(cfg.BrowserFilesRoot, relative)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", "", err
	}
	return directory, filepath.Join(cfg.BrowserFilesHostRoot, relative), nil
}

func browserSourcePath(cfg config.Config, source string) (string, error) {
	clean := filepath.Clean(source)
	if filepath.IsAbs(clean) {
		hostRoot := filepath.Clean(cfg.BrowserFilesHostRoot)
		if clean == hostRoot || strings.HasPrefix(clean, hostRoot+string(filepath.Separator)) {
			relative, _ := filepath.Rel(hostRoot, clean)
			clean = filepath.Join(cfg.BrowserFilesRoot, relative)
		}
	}
	root := filepath.Clean(cfg.BrowserFilesRoot)
	if clean != root && !strings.HasPrefix(clean, root+string(filepath.Separator)) {
		return "", errors.New("下载源不在浏览器交换目录内")
	}
	return clean, nil
}

func secureWorkspaceFile(workspace, source string) (string, error) {
	root, err := filepath.EvalSymlinks(filepath.Clean(workspace))
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(source)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(root, clean)
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil || resolved != clean || !pathInside(root, resolved) {
		return "", errors.New("源文件不在工作区内或路径包含符号链接")
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Size() > browserFileLimit {
		return "", errors.New("源文件必须是小于等于 25 MiB 的普通文件")
	}
	return resolved, nil
}

func secureWorkspaceDestination(workspace, destination string) (string, error) {
	root, err := filepath.EvalSymlinks(filepath.Clean(workspace))
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(destination)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(root, clean)
	}
	if clean == root || !pathInside(root, clean) {
		return "", errors.New("目标文件不在当前工作区内")
	}
	parent := filepath.Dir(clean)
	for current := parent; pathInside(root, current); current = filepath.Dir(current) {
		info, statErr := os.Lstat(current)
		if statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", errors.New("目标目录包含符号链接")
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		if current == root {
			break
		}
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	return clean, nil
}

func copyRegularFile(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	temporary, err := os.CreateTemp(filepath.Dir(target), ".browser-file-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := io.Copy(temporary, io.LimitReader(input, browserFileLimit+1)); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, target)
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func workerIPv4() (string, error) {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, address := range addresses {
		ip, _, parseErr := net.ParseCIDR(address.String())
		if parseErr == nil && ip.To4() != nil && !ip.IsLoopback() && ip.IsPrivate() {
			return ip.String(), nil
		}
	}
	return "", errors.New("worker 容器没有可用的私有 IPv4 地址")
}

func probePort(ctx context.Context, address string, port int) error {
	if port < 1 || port > 65535 {
		return errors.New("端口必须在 1 到 65535 之间")
	}
	dialer := net.Dialer{Timeout: time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(address, fmt.Sprintf("%d", port)))
	if err != nil {
		return fmt.Errorf("端口不可达，请确认开发服务监听 0.0.0.0:%d: %w", port, err)
	}
	return connection.Close()
}

func pathInside(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func browserJSONResult(value any) (codex.ToolCallResult, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return codex.ToolCallResult{}, err
	}
	return codex.TextToolResult(string(data), true), nil
}

func cleanupBrowserTask(cfg config.Config, taskID string) {
	if cfg.BrowserMCPURL != "" && taskID != "" {
		_ = os.RemoveAll(filepath.Join(cfg.BrowserFilesRoot, taskID))
	}
}

func sweepBrowserFiles(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-time.Hour)
	for _, entry := range entries {
		info, statErr := entry.Info()
		if statErr == nil && info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(root, entry.Name()))
		}
	}
}
