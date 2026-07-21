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

	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/workerprotocol"
	"go.uber.org/zap"
)

const workerVersion = "0.1.0"

type remoteTaskProcessor interface {
	ProcessRemote(context.Context, *workerprotocol.Task, <-chan workerprotocol.RunCommand,
		func(string, json.RawMessage)) (workerprotocol.CompleteRequest, error)
	ProcessDevelopmentOperation(context.Context, *workerprotocol.DevelopmentOperation) error
}

type RemoteRunner struct {
	cfg       config.Config
	client    *workerprotocol.Client
	processor remoteTaskProcessor
	logger    *zap.Logger
	journals  *journalStore
}

func NewRemoteRunner(cfg config.Config, client *workerprotocol.Client, processor remoteTaskProcessor,
	logger *zap.Logger,
) (*RemoteRunner, error) {
	journals, err := newJournalStore(cfg.WorkerDataRoot)
	if err != nil {
		return nil, err
	}
	return &RemoteRunner{cfg: cfg, client: client, processor: processor, logger: logger,
		journals: journals}, nil
}

func (r *RemoteRunner) Run(ctx context.Context) error {
	lock, err := acquireWorkerDataLock(r.cfg.WorkerDataRoot)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := r.authenticate(ctx); err != nil {
		return err
	}
	if err := r.sendHeartbeat(ctx); err != nil {
		return fmt.Errorf("首次节点心跳失败: %w", err)
	}
	go r.heartbeatLoop(ctx)

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
		slots <- struct{}{}
		active.Add(1)
		go r.runJournal(ctx, journal, slots, &active)
	}

	roles := r.roles()
	nextRole := 0
	for ctx.Err() == nil {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			active.Wait()
			return ctx.Err()
		}
		role := roles[nextRole%len(roles)]
		nextRole++
		claim, claimErr := r.client.Claim(ctx, workerprotocol.ClaimRequest{
			WorkerID: r.cfg.WorkerID, Role: role, Wait: true,
		})
		if claimErr != nil {
			<-slots
			r.logger.Warn("从 Control 领取任务失败", zap.Error(claimErr))
			if !waitContext(ctx, 3*time.Second) {
				break
			}
			continue
		}
		if claim.Task == nil && claim.DevelopmentOperation == nil {
			<-slots
			continue
		}
		if claim.DevelopmentOperation != nil {
			active.Add(1)
			go r.runDevelopmentOperation(ctx, claim.DevelopmentOperation, slots, &active)
			continue
		}
		task := claim.Task
		journal := &runJournal{Task: *task, NextSequence: 1}
		if err := r.journals.save(journal); err != nil {
			<-slots
			return fmt.Errorf("持久化新领取任务: %w", err)
		}
		active.Add(1)
		go r.runJournal(ctx, journal, slots, &active)
	}
	active.Wait()
	return ctx.Err()
}

func (r *RemoteRunner) authenticate(ctx context.Context) error {
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

func (r *RemoteRunner) roles() []string {
	if r.cfg.WorkerRole == "all" {
		return []string{"github", "discord"}
	}
	return []string{r.cfg.WorkerRole}
}

func (r *RemoteRunner) roleAllowed(source string) bool {
	return r.cfg.WorkerRole == "all" ||
		(r.cfg.WorkerRole == "github" && source == "github_work_item") ||
		(r.cfg.WorkerRole == "discord" && source == "discord_conversation")
}

func (r *RemoteRunner) sendHeartbeat(ctx context.Context) error {
	metadata, _ := json.Marshal(map[string]any{"workerId": r.cfg.WorkerID,
		"roles": r.roles(), "maxConcurrentJobs": r.cfg.WorkerMaxConcurrentJobs,
		"imageDigest": r.cfg.WorkerImageDigest, "protocolVersion": r.cfg.WorkerProtocolVersion})
	return r.client.Heartbeat(ctx, workerprotocol.HeartbeatRequest{
		WorkerVersion: workerVersion, ProtocolVersion: r.cfg.WorkerProtocolVersion,
		Metadata: metadata,
	})
}

func (r *RemoteRunner) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.sendHeartbeat(ctx); err != nil {
				r.logger.Warn("执行节点心跳失败", zap.Error(err))
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
