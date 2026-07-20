package hostdocker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/devenv"
	"go.uber.org/zap"
)

type commandRunner interface {
	Output(context.Context, ...string) (string, error)
}

type execRunner struct{ binary string }

func (r execRunner) Output(ctx context.Context, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, r.binary, arguments...)
	command.Env = append(os.Environ(), "DOCKER_HOST=unix://"+DockerSocket)
	output, err := command.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

type Manager struct {
	enabled         bool
	network         string
	root            string
	leaseDuration   time.Duration
	stopTimeout     time.Duration
	cleanupTimeout  time.Duration
	sweepInterval   time.Duration
	runner          commandRunner
	logger          *zap.Logger
	expectedVersion string
}

func NewManager(cfg config.Config, logger *zap.Logger) (*Manager, error) {
	lock, err := devenv.LoadRuntimeLock()
	if err != nil {
		return nil, err
	}
	manager := &Manager{
		enabled: cfg.EnableHostDocker, network: cfg.DockerNetwork,
		root: leaseRoot(cfg.WorkerDataRoot), leaseDuration: cfg.LeaseDuration,
		stopTimeout: cfg.DockerStopTimeout, cleanupTimeout: cfg.DockerCleanupTimeout,
		sweepInterval: cfg.DockerSweepInterval, runner: execRunner{binary: RealDockerBinary},
		logger: logger, expectedVersion: lock.DockerCLI,
	}
	if !manager.enabled {
		return manager, nil
	}
	if err := manager.validate(context.Background()); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(manager.root, 0o750); err != nil {
		return nil, fmt.Errorf("创建 Host Docker 状态目录: %w", err)
	}
	logger.Warn("Host Docker Beta 已启用。Beta。有安全风险，请确保所有用户可信再开启。")
	return manager, nil
}

func (m *Manager) Enabled() bool { return m != nil && m.enabled }

func (m *Manager) validate(ctx context.Context) error {
	info, err := os.Stat(DockerSocket)
	if err != nil {
		return fmt.Errorf("host Docker 已启用但 Socket 不可用: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("host Docker 路径 %s 不是 Unix Socket", DockerSocket)
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := m.runner.Output(checkCtx, "version", "--format", "{{.Client.Version}}|{{.Server.Version}}")
	if err != nil {
		return fmt.Errorf("连接宿主 Docker Daemon: %w: %s", err, output)
	}
	versions := strings.Split(output, "|")
	if len(versions) != 2 || versions[0] != m.expectedVersion || strings.TrimSpace(versions[1]) == "" {
		return fmt.Errorf("docker 版本无效，要求 Client %s，实际为 %q", m.expectedVersion, output)
	}
	return nil
}

type Session struct {
	manager *Manager
	scope   Scope
}

func (m *Manager) Begin(scope Scope) (*Session, error) {
	if !m.Enabled() {
		return nil, nil
	}
	if err := scope.validate(); err != nil {
		return nil, err
	}
	lease := runLease{Scope: scope, ExpiresAt: time.Now().UTC().Add(m.leaseDuration)}
	err := withLeaseLock(m.root, scope.RunID, func() error {
		existing, readErr := readLease(m.root, scope.RunID)
		if readErr == nil {
			lease.Containers = existing.Containers
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		return writeLease(m.root, lease)
	})
	if err != nil {
		return nil, err
	}
	return &Session{manager: m, scope: scope}, nil
}

func (s *Session) Environment() []string {
	if s == nil {
		return nil
	}
	return []string{
		"DOCKER_HOST=unix://" + DockerSocket,
		"TYRS_HAND_DOCKER_NETWORK=" + s.manager.network,
		"TYRS_HAND_DOCKER_WORKSPACE_ID=" + s.scope.WorkspaceID,
		"TYRS_HAND_DOCKER_INTENT_ID=" + s.scope.IntentID,
		"TYRS_HAND_DOCKER_RUN_ID=" + s.scope.RunID,
		"TYRS_HAND_DOCKER_NAME_PREFIX=" + s.scope.namePrefix(),
		envLeaseRoot + "=" + s.manager.root,
	}
}

func (s *Session) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	err := updateLease(s.manager.root, s.scope.RunID, func(lease *runLease) error {
		lease.Ended = true
		lease.ExpiresAt = time.Now().UTC()
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.manager.cleanupRun(ctx, s.scope.RunID)
}

func (m *Manager) Touch(runID string) error {
	if !m.Enabled() {
		return nil
	}
	err := updateLease(m.root, runID, func(lease *runLease) error {
		if !lease.Ended {
			lease.ExpiresAt = time.Now().UTC().Add(m.leaseDuration)
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (m *Manager) RunSweeper(ctx context.Context) {
	if !m.Enabled() {
		return
	}
	m.sweep(ctx)
	ticker := time.NewTicker(m.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.sweep(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (m *Manager) sweep(parent context.Context) {
	entries, err := filepath.Glob(filepath.Join(m.root, "*.json"))
	if err != nil {
		m.logger.Warn("扫描 Docker Run Lease 失败", zap.Error(err))
		return
	}
	for _, path := range entries {
		runID := strings.TrimSuffix(filepath.Base(path), ".json")
		lease, readErr := readLease(m.root, runID)
		if readErr != nil || lease.active(time.Now().UTC()) {
			continue
		}
		ctx, cancel := context.WithTimeout(parent, m.cleanupTimeout)
		cleanupErr := m.cleanupRun(ctx, runID)
		cancel()
		if cleanupErr != nil {
			m.logger.Warn("Host Docker 自动停止失败，将在下次扫描重试", zap.String("run_id", runID), zap.Error(cleanupErr))
		}
	}
}

func (m *Manager) cleanupRun(ctx context.Context, runID string) error {
	lease, err := readLease(m.root, runID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	candidates := append([]string{}, lease.Containers...)
	listed, listErr := m.runner.Output(ctx, "container", "ls", "--all", "--quiet",
		"--filter", "label="+managedLabel+"=true", "--filter", "label="+createdRunLabel+"="+runID)
	if listErr != nil {
		return fmt.Errorf("列出 Run 容器: %w: %s", listErr, listed)
	}
	for _, id := range strings.Fields(listed) {
		if !contains(candidates, id) {
			candidates = append(candidates, id)
		}
	}
	protected := m.protectedContainers(runID, time.Now().UTC())
	var failures []error
	for _, id := range candidates {
		if protected[id] {
			continue
		}
		if stopErr := m.stopManagedContainer(ctx, id); stopErr != nil {
			failures = append(failures, stopErr)
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	_ = os.Remove(leasePath(m.root, runID))
	_ = os.Remove(lockPath(m.root, runID))
	return nil
}

func (m *Manager) protectedContainers(excludedRun string, now time.Time) map[string]bool {
	protected := make(map[string]bool)
	paths, _ := filepath.Glob(filepath.Join(m.root, "*.json"))
	for _, path := range paths {
		runID := strings.TrimSuffix(filepath.Base(path), ".json")
		if runID == excludedRun {
			continue
		}
		lease, err := readLease(m.root, runID)
		if err == nil && lease.active(now) {
			for _, id := range lease.Containers {
				protected[id] = true
			}
		}
	}
	return protected
}

func (m *Manager) stopManagedContainer(ctx context.Context, id string) error {
	output, err := m.runner.Output(ctx, "container", "inspect", "--format",
		"{{ index .Config.Labels \""+managedLabel+"\" }}|{{ .State.Running }}", id)
	if err != nil {
		if missingContainer(output) {
			return nil
		}
		return fmt.Errorf("检查容器 %s: %w: %s", id, err, output)
	}
	if output != "true|true" {
		return nil
	}
	seconds := int(math.Ceil(m.stopTimeout.Seconds()))
	output, err = m.runner.Output(ctx, "container", "stop", "--time", strconv.Itoa(seconds), id)
	if err != nil && !missingContainer(output) {
		return fmt.Errorf("停止容器 %s: %w: %s", id, err, output)
	}
	return nil
}

func missingContainer(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "no such container") || strings.Contains(lower, "no such object")
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
