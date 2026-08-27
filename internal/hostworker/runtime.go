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
	EnvFile              string
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
	serviceProxy *serviceProxy
	ctx          context.Context
	cancel       context.CancelFunc
	start        func() (*runtimeGeneration, error)

	mu                  sync.Mutex
	closed              bool
	current             *runtimeGeneration
	generation          int64
	status              string
	done                chan struct{}
	waitErr             error
	generationChanged   chan int64
	nextBinding         atomic.Uint64
	toolHandlers        map[string]runtimeToolBinding
	interactiveHandlers map[string]runtimeInteractiveBinding
}

type runtimeGeneration struct {
	command *exec.Cmd
	hub     *appserverhub.Hub
	client  *appserverhub.Client
	done    chan struct{}
	waitErr error
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
	runtimeCtx, runtimeCancel := context.WithCancel(ctx)
	runtime := &Runtime{options: options, serviceProxy: serviceProxy, ctx: runtimeCtx,
		cancel: runtimeCancel, done: make(chan struct{}), status: "starting",
		generationChanged:   make(chan int64, 1),
		toolHandlers:        make(map[string]runtimeToolBinding),
		interactiveHandlers: make(map[string]runtimeInteractiveBinding)}
	runtime.start = runtime.launchGeneration
	generation, err := runtime.start()
	if err != nil {
		runtimeCancel()
		serviceProxy.close()
		return nil, err
	}
	runtime.activate(generation)
	go runtime.supervise(generation)
	return runtime, nil
}

func (r *Runtime) launchGeneration() (*runtimeGeneration, error) {
	socketPath := filepath.Join(r.options.StateDir, "app-server.sock")
	_ = os.Remove(socketPath)
	command := exec.Command(r.options.CodexBin,
		codex.HomeAppServerArguments("unix://"+socketPath)...)
	command.Dir = r.options.WorkspaceRoot
	values := map[string]string{"CODEX_HOME": r.options.CodexHome, "HOME": r.options.Home}
	envFile := r.options.EnvFile
	if envFile == "" {
		envFile = filepath.Join(r.options.StateDir, ".env")
	}
	secretValues, err := loadWorkerGlobalEnv(envFile)
	if err != nil {
		return nil, fmt.Errorf("读取 Worker Provider 密钥: %w", err)
	}
	for name, value := range secretValues {
		values[name] = value
	}
	if r.options.SSHAuthSock != "" {
		values["SSH_AUTH_SOCK"] = r.options.SSHAuthSock
	}
	if r.options.BrowserWorkerToken != "" {
		values[codex.BrowserMCPWorkerTokenEnvironment] = r.options.BrowserWorkerToken
	}
	if r.options.BrowserDesktopToken != "" {
		values[codex.BrowserMCPDesktopTokenEnvironment] = r.options.BrowserDesktopToken
	}
	command.Env = replaceEnvironment(appServerEnvironment(os.Environ()), values)
	command.Stdout, command.Stderr = r.options.CodexStdout, r.options.CodexStderr
	if command.Stdout == nil {
		command.Stdout = os.Stdout
	}
	if command.Stderr == nil {
		command.Stderr = os.Stderr
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("启动宿主 Codex App Server: %w", err)
	}
	generation := &runtimeGeneration{command: command, done: make(chan struct{})}
	go func() {
		generation.waitErr = command.Wait()
		close(generation.done)
	}()
	if err := waitSocket(r.ctx, socketPath, generation.done, 15*time.Second); err != nil {
		_ = command.Process.Kill()
		<-generation.done
		return nil, err
	}
	controller := r.options.Controller
	if controller == nil {
		controller = appserverhub.PassThroughController{}
	}
	hub, err := appserverhub.Start(r.ctx, appserverhub.Options{
		UpstreamSocketPath: socketPath, Controller: controller,
	})
	if err != nil {
		_ = command.Process.Kill()
		<-generation.done
		return nil, fmt.Errorf("启动 Worker AppServerHub: %w", err)
	}
	generation.hub = hub
	client, err := hub.OpenClient(appserverhub.ClientOptions{
		Role: appserverhub.RoleWorker, ServerRequestHandler: r.handleServerRequest,
	})
	if err != nil {
		_ = hub.Close()
		_ = command.Process.Kill()
		<-generation.done
		return nil, err
	}
	generation.client = client
	return generation, nil
}

func (r *Runtime) Client() *appserverhub.Client {
	client, _ := r.ClientSnapshot()
	return client
}

// ClientSnapshot 在同一把锁下返回当前可用的 Client 和世代，避免
// App Server 恢复切换时将旧 Client 误绑定到新世代。
func (r *Runtime) ClientSnapshot() (*appserverhub.Client, int64) {
	if r == nil {
		return nil, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.status != "running" || r.current == nil {
		return nil, r.generation
	}
	return r.current.client, r.generation
}

func (r *Runtime) activate(generation *runtimeGeneration) {
	r.mu.Lock()
	next := time.Now().UnixNano()
	if next <= r.generation {
		next = r.generation + 1
	}
	r.current, r.generation, r.status = generation, next, "running"
	r.mu.Unlock()
	select {
	case r.generationChanged <- next:
	default:
	}
}

func (r *Runtime) supervise(generation *runtimeGeneration) {
	defer func() {
		if r.serviceProxy != nil {
			r.serviceProxy.close()
		}
		r.mu.Lock()
		r.closed, r.status = true, "stopped"
		r.mu.Unlock()
		close(r.done)
	}()
	backoff := time.Second
	for {
		select {
		case <-r.ctx.Done():
			r.stopGeneration(generation, true)
			return
		case <-generation.done:
			r.stopGeneration(generation, false)
			r.mu.Lock()
			r.waitErr, r.status = generation.waitErr, "restarting"
			r.mu.Unlock()
			r.options.Logger.Error("宿主 Codex App Server 退出，Worker 保持运行并准备重启",
				zap.Error(generation.waitErr))
		}

		for {
			timer := time.NewTimer(backoff)
			select {
			case <-r.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			next, err := r.start()
			if err == nil {
				r.activate(next)
				r.options.Logger.Info("宿主 Codex App Server 已恢复")
				generation, backoff = next, time.Second
				break
			}
			r.mu.Lock()
			r.waitErr = err
			r.mu.Unlock()
			r.options.Logger.Warn("重启宿主 Codex App Server 失败", zap.Error(err),
				zap.Duration("retry_after", backoff))
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func (r *Runtime) stopGeneration(generation *runtimeGeneration, terminate bool) {
	if generation == nil {
		return
	}
	if generation.client != nil {
		_ = generation.client.Close()
	}
	if generation.hub != nil {
		_ = generation.hub.Close()
	}
	if !terminate || generation.command == nil || generation.command.Process == nil {
		return
	}
	select {
	case <-generation.done:
		return
	default:
	}
	_ = generation.command.Process.Signal(os.Interrupt)
	select {
	case <-generation.done:
	case <-time.After(5 * time.Second):
		_ = generation.command.Process.Kill()
		<-generation.done
	}
}

// Reload 请求宿主 Codex App Server 重新读取配置。Codex 当前通过 SIGHUP
// 处理配置刷新；若未来版本提供显式协议，可在此处替换为协议调用。
func (r *Runtime) Reload() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.current == nil || r.current.command == nil ||
		r.current.command.Process == nil || r.status != "running" {
		return errors.New("宿主 Codex App Server 不可用")
	}
	return r.current.command.Process.Signal(syscall.SIGHUP)
}

// OpenEphemeralClient 为 Worker 内部临时任务创建独立的 Desktop 事件域。
// 调用方只能用它创建 ephemeral Thread，并在任务结束后关闭 Client。
func (r *Runtime) OpenEphemeralClient() (*appserverhub.Client, error) {
	if r == nil {
		return nil, errors.New("宿主 Worker Runtime 不可用")
	}
	r.mu.Lock()
	if r.closed || r.current == nil || r.current.hub == nil || r.status != "running" {
		r.mu.Unlock()
		return nil, errors.New("worker AppServerHub 尚未启动")
	}
	hub := r.current.hub
	r.mu.Unlock()
	return hub.OpenClient(appserverhub.ClientOptions{Role: appserverhub.RoleDesktop})
}

func (r *Runtime) CodexHome() string { return r.options.CodexHome }

func (r *Runtime) Home() string { return r.options.Home }

func (r *Runtime) WorkspaceRoot() string { return r.options.WorkspaceRoot }

func (r *Runtime) StateDir() string { return r.options.StateDir }

func (r *Runtime) Generation() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.generation
}

func (r *Runtime) Status() string {
	if r == nil {
		return "unavailable"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == "" {
		return "unavailable"
	}
	return r.status
}

func (r *Runtime) GenerationChanges() <-chan int64 { return r.generationChanged }

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
	r.mu.Lock()
	if r.current == nil || r.current.hub == nil || r.status != "running" {
		r.mu.Unlock()
		return errors.New("worker AppServerHub 尚未启动")
	}
	hub := r.current.hub
	r.mu.Unlock()
	return hub.ServeConn(connection)
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		<-r.done
		return nil
	}
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	<-r.done
	return nil
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

func loadWorkerGlobalEnv(path string) (map[string]string, error) {
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
		if !ok || (name != "TYRS_HAND_MODEL_API_KEY" && name != "TYRS_HAND_MODEL_BASE_URL") {
			continue
		}
		if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
			value = value[1 : len(value)-1]
		}
		result[name] = value
	}
	return result, nil
}
