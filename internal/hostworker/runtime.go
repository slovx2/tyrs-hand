package hostworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"go.uber.org/zap"
)

type RuntimeOptions struct {
	CodexBin             string
	CodexHome            string
	Home                 string
	WorkspaceRoot        string
	StateDir             string
	SSHAuthSock          string
	BrowserWorkerToken   string
	BrowserDesktopToken  string
	BrowserServiceSocket string
	CodexStdout          io.Writer
	CodexStderr          io.Writer
	Controller           appserverhub.Controller
	Logger               *zap.Logger
}

type Runtime struct {
	options      RuntimeOptions
	command      *exec.Cmd
	hub          *appserverhub.Hub
	client       *appserverhub.Client
	serviceProxy *serviceProxy
	generation   int64

	mu                  sync.Mutex
	closed              bool
	done                chan struct{}
	waitErr             error
	nextBinding         atomic.Uint64
	toolHandlers        map[string]runtimeToolBinding
	interactiveHandlers map[string]runtimeInteractiveBinding
}

type runtimeToolBinding struct {
	id      uint64
	handler codex.ToolHandler
}

type runtimeInteractiveBinding struct {
	id      uint64
	handler codex.ServerRequestHandler
}

func StartRuntime(ctx context.Context, options RuntimeOptions) (*Runtime, error) {
	if options.CodexBin == "" || options.CodexHome == "" || options.Home == "" ||
		options.WorkspaceRoot == "" || options.StateDir == "" {
		return nil, errors.New("宿主 Worker 的 Codex 路径、Home、工作区和状态目录不能为空")
	}
	if options.Logger == nil {
		options.Logger = zap.NewNop()
	}
	versionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := codex.ValidateVersion(versionCtx, options.CodexBin)
	cancel()
	if err != nil {
		return nil, err
	}
	for _, directory := range []string{options.CodexHome, options.WorkspaceRoot, options.StateDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
	}
	serviceProxy, err := startServiceProxy(options.BrowserServiceSocket)
	if err != nil {
		return nil, fmt.Errorf("启动浏览器服务代理: %w", err)
	}
	socketPath := filepath.Join(options.StateDir, "app-server.sock")
	_ = os.Remove(socketPath)
	command := exec.Command(options.CodexBin,
		codex.HomeAppServerArguments("unix://"+socketPath)...)
	command.Dir = options.WorkspaceRoot
	environment := appServerEnvironment(os.Environ())
	values := map[string]string{
		"CODEX_HOME": options.CodexHome,
		"HOME":       options.Home,
	}
	if secretValues, err := loadSecretEnv(filepath.Join(options.StateDir, ".env")); err != nil {
		return nil, fmt.Errorf("读取 Worker Provider 密钥: %w", err)
	} else {
		for name, value := range secretValues {
			values[name] = value
		}
	}
	if options.SSHAuthSock != "" {
		values["SSH_AUTH_SOCK"] = options.SSHAuthSock
	}
	if options.BrowserWorkerToken != "" {
		values[codex.BrowserMCPWorkerTokenEnvironment] = options.BrowserWorkerToken
	}
	if options.BrowserDesktopToken != "" {
		values[codex.BrowserMCPDesktopTokenEnvironment] = options.BrowserDesktopToken
	}
	command.Env = replaceEnvironment(environment, values)
	command.Stdout = options.CodexStdout
	if command.Stdout == nil {
		command.Stdout = os.Stdout
	}
	command.Stderr = options.CodexStderr
	if command.Stderr == nil {
		command.Stderr = os.Stderr
	}
	if err := command.Start(); err != nil {
		serviceProxy.close()
		return nil, fmt.Errorf("启动宿主 Codex App Server: %w", err)
	}
	runtime := &Runtime{options: options, command: command, done: make(chan struct{}),
		serviceProxy:        serviceProxy,
		generation:          time.Now().UnixNano(),
		toolHandlers:        make(map[string]runtimeToolBinding),
		interactiveHandlers: make(map[string]runtimeInteractiveBinding)}
	go runtime.wait()
	if err := waitSocket(ctx, socketPath, runtime.done, 15*time.Second); err != nil {
		_ = command.Process.Kill()
		<-runtime.done
		serviceProxy.close()
		return nil, err
	}
	controller := options.Controller
	if controller == nil {
		controller = appserverhub.PassThroughController{}
	}
	hub, err := appserverhub.Start(ctx, appserverhub.Options{
		UpstreamSocketPath: socketPath,
		Controller:         controller,
	})
	if err != nil {
		_ = command.Process.Kill()
		<-runtime.done
		serviceProxy.close()
		return nil, fmt.Errorf("启动 Worker AppServerHub: %w", err)
	}
	runtime.hub = hub
	client, err := hub.OpenClient(appserverhub.ClientOptions{
		Role: appserverhub.RoleWorker, ServerRequestHandler: runtime.handleServerRequest,
	})
	if err != nil {
		_ = hub.Close()
		_ = command.Process.Kill()
		<-runtime.done
		serviceProxy.close()
		return nil, err
	}
	runtime.client = client
	return runtime, nil
}

func (r *Runtime) Client() *appserverhub.Client { return r.client }

// Reload 请求宿主 Codex App Server 重新读取配置。Codex 当前通过 SIGHUP
// 处理配置刷新；若未来版本提供显式协议，可在此处替换为协议调用。
func (r *Runtime) Reload() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.command == nil || r.command.Process == nil {
		return errors.New("宿主 Codex App Server 不可用")
	}
	return r.command.Process.Signal(syscall.SIGHUP)
}

// OpenEphemeralClient 为 Worker 内部临时任务创建独立的 Desktop 事件域。
// 调用方只能用它创建 ephemeral Thread，并在任务结束后关闭 Client。
func (r *Runtime) OpenEphemeralClient() (*appserverhub.Client, error) {
	if r == nil {
		return nil, errors.New("宿主 Worker Runtime 不可用")
	}
	r.mu.Lock()
	if r.closed || r.hub == nil {
		r.mu.Unlock()
		return nil, errors.New("worker AppServerHub 尚未启动")
	}
	hub := r.hub
	r.mu.Unlock()
	return hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleDesktop})
}

func (r *Runtime) CodexHome() string { return r.options.CodexHome }

func (r *Runtime) Home() string { return r.options.Home }

func (r *Runtime) WorkspaceRoot() string { return r.options.WorkspaceRoot }

func (r *Runtime) StateDir() string { return r.options.StateDir }

func (r *Runtime) Generation() int64 { return r.generation }

func (r *Runtime) Done() <-chan struct{} { return r.done }

func (r *Runtime) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.waitErr
}

func (r *Runtime) BindTool(threadID string, handler codex.ToolHandler) func() {
	id := r.nextBinding.Add(1)
	r.mu.Lock()
	r.toolHandlers[threadID] = runtimeToolBinding{id: id, handler: handler}
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		if current := r.toolHandlers[threadID]; current.id == id {
			delete(r.toolHandlers, threadID)
		}
		r.mu.Unlock()
	}
}

func (r *Runtime) BindInteractive(threadID string, handler codex.ServerRequestHandler) func() {
	id := r.nextBinding.Add(1)
	r.mu.Lock()
	r.interactiveHandlers[threadID] = runtimeInteractiveBinding{id: id, handler: handler}
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		if current := r.interactiveHandlers[threadID]; current.id == id {
			delete(r.interactiveHandlers, threadID)
		}
		r.mu.Unlock()
	}
}

func (r *Runtime) handleServerRequest(ctx context.Context, request codex.ServerRequest) (any, error) {
	var scope struct {
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal(request.Params, &scope); err != nil {
		return nil, fmt.Errorf("解析 Codex Server Request: %w", err)
	}
	r.mu.Lock()
	tool := r.toolHandlers[scope.ThreadID].handler
	interactive := r.interactiveHandlers[scope.ThreadID].handler
	r.mu.Unlock()
	switch request.Method {
	case "item/tool/call":
		var call codex.ToolCallRequest
		if err := json.Unmarshal(request.Params, &call); err != nil {
			return nil, fmt.Errorf("解析动态工具请求: %w", err)
		}
		if tool == nil {
			return nil, errors.New("当前 Thread 没有活动的工具授权")
		}
		return tool(ctx, call)
	case "item/tool/requestUserInput":
		if interactive == nil {
			return nil, errors.New("当前 Thread 没有活动的交互控制器")
		}
		return interactive(ctx, request)
	default:
		return nil, fmt.Errorf("worker 尚未支持 Codex Server Request %q", request.Method)
	}
}

func (r *Runtime) ServeDesktop(connection net.Conn) error {
	if r.hub == nil {
		return errors.New("worker AppServerHub 尚未启动")
	}
	return r.hub.ServeConn(connection)
}

func (r *Runtime) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		<-r.done
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	if r.client != nil {
		_ = r.client.Close()
	}
	if r.hub != nil {
		_ = r.hub.Close()
	}
	if r.command != nil && r.command.Process != nil {
		_ = r.command.Process.Signal(os.Interrupt)
		select {
		case <-r.done:
		case <-time.After(5 * time.Second):
			_ = r.command.Process.Kill()
			<-r.done
		}
	}
	r.serviceProxy.close()
	return nil
}

func (r *Runtime) wait() {
	err := r.command.Wait()
	r.mu.Lock()
	r.waitErr = err
	r.mu.Unlock()
	close(r.done)
}

func waitSocket(ctx context.Context, path string, processDone <-chan struct{}, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		connection, err := net.DialTimeout("unix", path, 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-processDone:
			return errors.New("codex App Server 在 Socket 就绪前退出")
		case <-deadline.C:
			return errors.New("等待 Codex App Server Socket 超时")
		case <-ticker.C:
		}
	}
}

func replaceEnvironment(base []string, values map[string]string) []string {
	result := make([]string, 0, len(base)+len(values))
	for _, item := range base {
		name, _, found := cutEnvironment(item)
		if found {
			if _, managed := values[name]; managed {
				continue
			}
		}
		result = append(result, item)
	}
	for name, value := range values {
		result = append(result, name+"="+value)
	}
	return result
}

func appServerEnvironment(base []string) []string {
	result := make([]string, 0, len(base))
	for _, item := range base {
		name, _, found := cutEnvironment(item)
		if !found || strings.HasPrefix(name, "TYRS_HAND_") {
			continue
		}
		switch name {
		case "OPENAI_ORG_ID", "OPENAI_PROJECT_ID", "CODEX_API_KEY",
			"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
			"http_proxy", "https_proxy", "all_proxy", "no_proxy",
			codex.BrowserMCPWorkerTokenEnvironment,
			codex.BrowserMCPDesktopTokenEnvironment:
			continue
		}
		result = append(result, item)
	}
	return result
}

func cutEnvironment(value string) (string, string, bool) {
	for index := range value {
		if value[index] == '=' {
			return value[:index], value[index+1:], true
		}
	}
	return "", "", false
}

func loadSecretEnv(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		name, value, ok := cutEnvironment(line)
		if !ok || name != "TYRS_HAND_MODEL_API_KEY" {
			continue
		}
		result[name] = value
	}
	return result, nil
}
