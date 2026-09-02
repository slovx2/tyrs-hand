package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

var workerVersion = "dev"

type taskProcessor interface {
	Process(context.Context, *workerprotocol.Task, <-chan workerprotocol.RunCommand,
		func(string, json.RawMessage)) (workerprotocol.CompleteRequest, error)
}

type heartbeatMetadataProvider interface {
	HeartbeatMetadata() map[string]any
}

type Runner struct {
	cfg                   config.Config
	client                *workerprotocol.Client
	processor             taskProcessor
	logger                *zap.Logger
	journals              *journalStore
	ssh                   *sshAgentManager
	browser               *browserHealthMonitor
	coordinator           *runCoordinator
	sshHostKeyFingerprint string
}

func (r *Runner) SetSSHHostKeyFingerprint(fingerprint string) {
	r.sshHostKeyFingerprint = fingerprint
}

func NewRunner(cfg config.Config, client *workerprotocol.Client, processor taskProcessor,
	logger *zap.Logger,
) (*Runner, error) {
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
	runner := &Runner{cfg: cfg, client: client, processor: processor, logger: logger,
		journals: journals, coordinator: coordinator}
	if cfg.EnableSSH {
		runner.ssh = newSSHAgentManager(cfg.SSHAgentDir, client, logger)
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
	slots := make(chan struct{}, r.cfg.WorkerMaxConcurrentJobs)
	var active sync.WaitGroup
	stored, err := r.journals.loadAll()
	if err != nil {
		return err
	}
	for _, journal := range stored {
		if !r.roleAllowed(journal.Task.Claimed.SourceType) {
			return fmt.Errorf("run Journal %s 与当前 Worker 角色不匹配",
				journal.Task.Claimed.RunID)
		}
		commands := make(chan workerprotocol.RunCommand, 16)
		r.coordinator.register(journal, commands)
		slots <- struct{}{}
		logger := r.logger.With(zap.String("run_id", journal.Task.Claimed.RunID.String()))
		_ = r.syncRunState(ctx, journal, commands, logger)
		_ = r.flushEvents(ctx, journal, logger)
		active.Add(1)
		go r.runJournal(ctx, journal, commands, slots, &active)
	}
	if err := r.sendHeartbeat(ctx); err != nil {
		r.logger.Warn("首次节点心跳失败，本地任务继续运行", zap.Error(err))
	}
	go r.heartbeatLoop(ctx)

	for ctx.Err() == nil {
		claim, claimErr := r.client.Claim(ctx, workerprotocol.ClaimRequest{
			Role: r.claimRole(), Wait: true,
		})
		if claimErr != nil {
			r.logger.Warn("从 Control 领取任务失败", zap.Error(claimErr))
			if !waitContext(ctx, 3*time.Second) {
				break
			}
			continue
		}
		if claim.Task == nil {
			continue
		}
		task := claim.Task
		if activeTask, routed, applied := r.coordinator.route(task); routed {
			if applied {
				decisionTask := *task
				decisionTask.Claimed.RunID = activeTask.Claimed.RunID
				requestCtx, cancel := context.WithTimeout(ctx, r.cfg.ControlTimeout)
				_ = r.client.DecideInput(requestCtx, &decisionTask,
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
		if err := r.journals.save(journal); err != nil {
			<-slots
			return fmt.Errorf("持久化新领取任务: %w", err)
		}
		commands := make(chan workerprotocol.RunCommand, 16)
		r.coordinator.register(journal, commands)
		active.Add(1)
		go r.runJournal(ctx, journal, commands, slots, &active)
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
		return nil
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
	return nil
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
	values := map[string]any{"workerId": r.cfg.WorkerID,
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
	metadata, _ := json.Marshal(values)
	return r.client.Heartbeat(ctx, workerprotocol.HeartbeatRequest{
		WorkerVersion: workerVersion, ProtocolVersion: r.cfg.WorkerProtocolVersion,
		SSHHostKeyFingerprint: r.sshHostKeyFingerprint, Metadata: metadata,
	})
}

func (r *Runner) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.HeartbeatInterval)
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".credential-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(credential); err != nil {
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
