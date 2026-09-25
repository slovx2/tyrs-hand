package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/runtimeidentity"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

var workerVersion = "dev"

// catalogHeartbeatRefresh 是心跳重新上传完整模型目录的最长间隔。
// 目录通常只在 Provider 变化时改变，因此其余心跳只上报 revision。
const catalogHeartbeatRefresh = 30 * time.Minute

type taskProcessor interface {
	Process(context.Context, *workerprotocol.Task, <-chan workerprotocol.RunCommand,
		func(string, json.RawMessage)) (workerprotocol.CompleteRequest, error)
}

type heartbeatMetadataProvider interface {
	HeartbeatMetadata() map[string]any
}

type Runner struct {
	*runtimeExecutor
	workerID              uuid.UUID
	ssh                   *sshAgentManager
	browser               *browserHealthMonitor
	sshHostKeyFingerprint string
	turnSlots             chan struct{}
	runtimeReports        func() []workerprotocol.RuntimeReport
	executors             []*runtimeExecutor
	nextExecutor          int

	catalogMu         sync.Mutex
	catalogRevision   string
	catalogRevisionAt time.Time
}

func (r *Runner) SetSSHHostKeyFingerprint(fingerprint string) {
	r.sshHostKeyFingerprint = fingerprint
}

// 在 Run 前绑定；心跳读取当下的引擎状态，不缓存重启前的状态。
func (r *Runner) SetRuntimeReports(provider func() []workerprotocol.RuntimeReport) {
	r.runtimeReports = provider
}

// NotifyControlWake 接收 Control 推送的唤醒种类。
func (r *Runner) NotifyControlWake(kinds []string) {
	for _, executor := range r.executors {
		executor.wake.Notify(kinds)
	}
}

// SetControlChannelState 记录控制通道的连接与唤醒能力状态。
func (r *Runner) SetControlChannelState(connected, wake bool) {
	for _, executor := range r.executors {
		executor.wake.SetState(connected, wake)
	}
}

func NewRunner(cfg config.Config, client *workerprotocol.Client, processor taskProcessor,
	logger *zap.Logger,
) (*Runner, error) {
	if client == nil || client.Engine() != runtimeidentity.Codex {
		return nil, errors.New("Worker 调度器需要 Codex 主客户端管理共享身份")
	}
	if cfg.NodeHeartbeatInterval <= 0 {
		cfg.NodeHeartbeatInterval = time.Minute
	}
	if cfg.WorkerClaimFallbackInterval <= 0 {
		cfg.WorkerClaimFallbackInterval = time.Minute
	}
	if cfg.WorkerSyncFallbackInterval <= 0 {
		cfg.WorkerSyncFallbackInterval = 5 * time.Minute
	}
	var journals *journalStore
	var coordinator *runCoordinator
	var err error
	if concrete, ok := processor.(*Processor); ok && concrete.journals != nil {
		journals, coordinator = concrete.journals, concrete.coordinator
	} else {
		journals, err = newJournalStore(cfg.WorkerDataRoot)
		if err != nil {
			return nil, err
		}
		coordinator = newRunCoordinator(journals)
	}
	executor := &runtimeExecutor{engine: runtimeidentity.Codex, cfg: cfg, client: client,
		processor: processor, logger: logger, journals: journals, coordinator: coordinator}
	runner := &Runner{runtimeExecutor: executor, executors: []*runtimeExecutor{executor}}
	if concrete, ok := processor.(*Processor); ok {
		runner.turnSlots = concrete.turnSlots
	} else {
		runner.turnSlots = make(chan struct{}, max(1, cfg.WorkerMaxConcurrentJobs))
	}
	if concrete, ok := processor.(*Processor); ok && concrete.wake != nil {
		runner.wake = concrete.wake
	} else {
		runner.wake = newWakeSignals()
	}
	executor.claimWake = runner.wake
	if cfg.EnableSSH && cfg.ControlSyncEnabled() {
		runner.ssh = newSSHAgentManager(cfg.SSHAgentDir, client, runner.wake,
			cfg.WorkerSyncFallbackInterval, logger)
	}
	if cfg.BrowserMCPURL != "" {
		runner.browser, err = newBrowserHealthMonitor(cfg.BrowserMCPURL)
		if err != nil {
			return nil, err
		}
	}
	return runner, nil
}

func (r *Runner) Run(ctx context.Context) error {
	if !r.cfg.ControlSyncEnabled() {
		r.logger.Info("已关闭与 Control 的通信，仅保留本地 SSH 与 Codex")
		<-ctx.Done()
		return ctx.Err()
	}
	if err := r.Authenticate(ctx); err != nil {
		return err
	}
	if r.ssh != nil {
		go func() {
			if err := r.ssh.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				r.logger.Error("SSH Agent 管理器停止", zap.Error(err))
			}
		}()
		defer r.ssh.Close()
	}
	slots := r.turnSlots
	var active sync.WaitGroup
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		active.Wait()
	}()
	if err := r.recoverJournals(ctx, &active); err != nil {
		return err
	}
	if err := r.sendHeartbeat(ctx); err != nil {
		r.logger.Warn("首次节点心跳失败，本地任务继续运行", zap.Error(err))
	}
	go r.heartbeatLoop(ctx)

	for ctx.Err() == nil {
		if !r.wake.Wait(ctx, r.cfg.WorkerClaimFallbackInterval, workerprotocol.WakeClaim) {
			break
		}
		executor, task := r.claimNext(ctx)
		if task == nil {
			continue
		}
		// 领取成功后立刻再检查一次，直到队列为空，避免依赖下一次唤醒。
		r.wake.Notify([]string{workerprotocol.WakeClaim})
		if activeTask, routed, applied := executor.coordinator.route(task); routed {
			if applied {
				decisionTask := *task
				decisionTask.Claimed.RunID = activeTask.Claimed.RunID
				requestCtx, cancel := context.WithTimeout(ctx, r.cfg.ControlTimeout)
				_ = executor.client.DecideInput(requestCtx, &decisionTask,
					resolvedCommandAction(task.Claimed.Operation),
					activeTask.Claimed.ConfirmedTurnID)
				cancel()
			}
			if !waitContext(ctx, 100*time.Millisecond) {
				break
			}
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			active.Wait()
			return ctx.Err()
		}
		task.Claimed.RunID = uuid.New()
		journal := &runJournal{Task: *task, NextSequence: 1}
		if err := executor.journals.save(journal); err != nil {
			<-slots
			return fmt.Errorf("持久化新领取任务: %w", err)
		}
		commands := make(chan workerprotocol.RunCommand, 16)
		executor.coordinator.register(journal, commands)
		active.Add(1)
		go executor.runJournal(ctx, journal, commands, slots, &active)
	}
	active.Wait()
	return ctx.Err()
}

func resolvedCommandAction(operation string) string {
	if operation == "interrupt" || operation == "replace_last_turn" {
		return "interrupt"
	}
	return "steer"
}

func (r *Runner) Authenticate(ctx context.Context) error {
	credential, err := readCredential(r.cfg.WorkerCredentialFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if credential != "" {
		r.client.SetCredential(credential)
		return r.resolveAuthenticatedIdentity(ctx, credential)
	}
	if r.cfg.WorkerEnrollmentToken == "" {
		return errors.New("节点尚未注册，且没有提供一次性 Enrollment Token")
	}
	response, err := r.client.Enroll(ctx, r.cfg.WorkerEnrollmentToken)
	if err != nil {
		return err
	}
	if response.ProtocolVersion != r.cfg.WorkerProtocolVersion {
		return fmt.Errorf("control 协议版本为 %d，Worker 配置为 %d",
			response.ProtocolVersion, r.cfg.WorkerProtocolVersion)
	}
	if err := writeCredential(r.cfg.WorkerCredentialFile, response.Credential); err != nil {
		return err
	}
	r.client.SetCredential(response.Credential)
	return r.saveIdentity(response.WorkerID, response.Credential)
}

func (r *Runner) roles() []string {
	if r.cfg.WorkerRole == "all" {
		return []string{"discord"}
	}
	return []string{r.cfg.WorkerRole}
}

func (r *Runner) claimRole() string {
	if r.cfg.WorkerRole == "all" {
		return "discord"
	}
	return r.cfg.WorkerRole
}

func (r *Runner) roleAllowed(source string) bool {
	return (r.cfg.WorkerRole == "all" && source == "workspace_session") ||
		(r.cfg.WorkerRole == "discord" && source == "workspace_session")
}

func (r *Runner) sendHeartbeat(ctx context.Context) error {
	values := map[string]any{"workerId": r.WorkerID(),
		"roles": r.roles(), "maxConcurrentJobs": r.cfg.WorkerMaxConcurrentJobs,
		"protocolVersion": r.cfg.WorkerProtocolVersion}
	values["ssh"] = map[string]any{"status": "ready",
		"listenAddress": r.cfg.WorkerSSHListenAddr}
	if r.ssh != nil {
		values["outboundSSH"] = r.ssh.Status()
	}
	if r.browser != nil {
		healthCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		r.browser.Refresh(healthCtx)
		cancel()
		values["browser"] = r.browser.Status()
		sweepBrowserFiles(r.cfg.BrowserFilesRoot)
	}
	if provider, ok := r.processor.(heartbeatMetadataProvider); ok {
		for key, value := range provider.HeartbeatMetadata() {
			values[key] = value
		}
	}
	revision, catalogIncluded := r.applyCatalogRevision(values)
	metadata, _ := json.Marshal(values)
	var runtimes []workerprotocol.RuntimeReport
	if r.runtimeReports != nil {
		runtimes = r.runtimeReports()
	}
	if err := r.client.Heartbeat(ctx, workerprotocol.HeartbeatRequest{
		Runtimes:      runtimes,
		WorkerVersion: workerVersion, ProtocolVersion: r.cfg.WorkerProtocolVersion,
		SSHHostKeyFingerprint: r.sshHostKeyFingerprint,
		ModelCatalogRevision:  revision, Metadata: metadata,
	}); err != nil {
		return err
	}
	if catalogIncluded {
		r.catalogMu.Lock()
		r.catalogRevision = revision
		r.catalogRevisionAt = time.Now()
		r.catalogMu.Unlock()
	}
	return nil
}

// applyCatalogRevision 决定本次心跳是否携带完整模型目录。
// 只有目录内容变化、首次上报或超过刷新间隔时才上传正文，
// 其余心跳仅带 revision，由 Control 保留上一份快照。
func (r *Runner) applyCatalogRevision(values map[string]any) (string, bool) {
	raw, ok := values["modelCatalog"].(json.RawMessage)
	r.catalogMu.Lock()
	defer r.catalogMu.Unlock()
	if !ok || len(raw) == 0 {
		delete(values, "modelCatalog")
		if r.catalogRevision != "" {
			// 目录被清空时显式发送 null，让 Control 删除旧快照。
			values["modelCatalog"] = nil
			return "", true
		}
		return "", false
	}
	digest := sha256.Sum256(raw)
	revision := hex.EncodeToString(digest[:])
	included := revision != r.catalogRevision ||
		time.Since(r.catalogRevisionAt) >= catalogHeartbeatRefresh
	if !included {
		delete(values, "modelCatalog")
	}
	return revision, included
}

func (r *Runner) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.NodeHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.sendHeartbeat(ctx); err != nil {
				r.logger.Warn("Worker心跳失败", zap.Error(err))
			}
		}
	}
}

func readCredential(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("节点凭据文件权限必须是 0600")
	}
	data, err := os.ReadFile(path)
	return string(data), err
}

func writeCredential(path, credential string) error {
	return writePrivateStateFile(path, []byte(credential))
}

func writePrivateStateFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(duration):
		return true
	}
}
