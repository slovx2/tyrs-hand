package gitworkspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/slovx2/tyrs-hand/internal/ports"
)

type Manager struct {
	cacheRoot    string
	worktreeRoot string
}

func NewManager(cacheRoot, worktreeRoot string) *Manager {
	return &Manager{cacheRoot: cacheRoot, worktreeRoot: worktreeRoot}
}

func (m *Manager) Ensure(ctx context.Context, spec ports.WorkspaceSpec, credential string) (ports.Workspace, error) {
	if err := validateID(spec.RepositoryID); err != nil {
		return ports.Workspace{}, err
	}
	if err := validateID(spec.WorkItemID); err != nil {
		return ports.Workspace{}, err
	}
	if spec.CloneURL == "" || spec.BaseRef == "" || spec.Branch == "" {
		return ports.Workspace{}, errors.New("创建 Worktree 缺少 CloneURL、BaseRef 或 Branch")
	}
	cachePath := filepath.Join(m.cacheRoot, spec.RepositoryID, "repository.git")
	worktreePath := filepath.Join(m.worktreeRoot, spec.WorkItemID)
	lockPath := filepath.Join(m.cacheRoot, spec.RepositoryID, ".lock")
	unlock, err := lock(lockPath)
	if err != nil {
		return ports.Workspace{}, err
	}
	defer unlock()

	if _, err := os.Stat(cachePath); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(cachePath), 0o750); err != nil {
			return ports.Workspace{}, err
		}
		if _, err := runGit(ctx, "", credential, "clone", "--bare", "--", spec.CloneURL, cachePath); err != nil {
			return ports.Workspace{}, fmt.Errorf("创建 Bare Cache: %w", err)
		}
	} else if err != nil {
		return ports.Workspace{}, err
	} else {
		if _, err := runGit(ctx, cachePath, credential, "remote", "set-url", "origin", spec.CloneURL); err != nil {
			return ports.Workspace{}, err
		}
	}
	if _, err := runGit(ctx, cachePath, credential, "fetch", "--prune", "origin", "+refs/heads/*:refs/remotes/origin/*", "+refs/pull/*/head:refs/remotes/pull/*"); err != nil {
		return ports.Workspace{}, fmt.Errorf("更新 Bare Cache: %w", err)
	}

	if info, err := os.Stat(worktreePath); err == nil && info.IsDir() {
		valid, err := validWorktree(ctx, cachePath, worktreePath, spec.Branch)
		if err != nil {
			return ports.Workspace{}, err
		}
		if valid {
			head, err := revParse(ctx, worktreePath)
			return ports.Workspace{CachePath: cachePath, WorktreePath: worktreePath, Branch: spec.Branch, HeadSHA: head}, err
		}
		if err := quarantineWorktree(ctx, cachePath, worktreePath); err != nil {
			return ports.Workspace{}, err
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ports.Workspace{}, err
	}
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o750); err != nil {
		return ports.Workspace{}, err
	}
	if _, err := runGit(ctx, cachePath, "", "worktree", "add", "-B", spec.Branch, worktreePath, spec.BaseRef); err != nil {
		return ports.Workspace{}, fmt.Errorf("创建 Worktree: %w", err)
	}
	head, err := revParse(ctx, worktreePath)
	if err != nil {
		return ports.Workspace{}, err
	}
	return ports.Workspace{CachePath: cachePath, WorktreePath: worktreePath, Branch: spec.Branch, HeadSHA: head}, nil
}

func validWorktree(ctx context.Context, cachePath, worktreePath, branch string) (bool, error) {
	commonDir, err := runGit(ctx, worktreePath, "", "rev-parse", "--git-common-dir")
	if err != nil {
		return false, nil
	}
	actualCommonDir, err := canonicalPath(worktreePath, strings.TrimSpace(commonDir))
	if err != nil {
		return false, err
	}
	expectedCommonDir, err := canonicalPath("", cachePath)
	if err != nil {
		return false, err
	}
	if actualCommonDir != expectedCommonDir {
		return false, nil
	}
	currentBranch, err := runGit(ctx, worktreePath, "", "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return false, nil
	}
	return strings.TrimSpace(currentBranch) == branch, nil
}

func canonicalPath(base, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved, nil
	}
	return absolute, nil
}

func quarantineWorktree(ctx context.Context, cachePath, worktreePath string) error {
	destination := fmt.Sprintf("%s.quarantine-%d", worktreePath, time.Now().UTC().UnixNano())
	if err := os.Rename(worktreePath, destination); err != nil {
		return fmt.Errorf("隔离不可信 Worktree: %w", err)
	}
	if _, err := runGit(ctx, cachePath, "", "worktree", "prune", "--expire=now"); err != nil {
		return fmt.Errorf("清理已隔离 Worktree 元数据: %w", err)
	}
	return nil
}

func (m *Manager) Status(ctx context.Context, worktreePath string) (string, error) {
	return runGit(ctx, worktreePath, "", "status", "--porcelain=v1", "--branch")
}

func (m *Manager) Commit(ctx context.Context, worktreePath, message string) (string, error) {
	message = strings.TrimSpace(message)
	if message == "" || len(message) > 200 {
		return "", errors.New("提交信息必须为 1 到 200 个字符")
	}
	status, err := runGit(ctx, worktreePath, "", "status", "--porcelain=v1")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(status) == "" {
		return "", errors.New("当前 Worktree 没有可提交的变更")
	}
	if _, err := runGit(ctx, worktreePath, "", "add", "--all"); err != nil {
		return "", err
	}
	if _, err := runGit(ctx, worktreePath, "", "-c", "user.name=TyrsHand Agent", "-c", "user.email=tyrs-hand[bot]@users.noreply.github.com", "commit", "-m", message); err != nil {
		return "", fmt.Errorf("创建提交: %w", err)
	}
	return revParse(ctx, worktreePath)
}

func (m *Manager) Publish(ctx context.Context, worktreePath, remoteBranch, credential string) (string, error) {
	if strings.TrimSpace(remoteBranch) == "" || strings.HasPrefix(remoteBranch, "-") {
		return "", errors.New("远程分支名无效")
	}
	head, err := revParse(ctx, worktreePath)
	if err != nil {
		return "", err
	}
	if _, err := runGit(ctx, worktreePath, credential, "push", "origin", "HEAD:refs/heads/"+remoteBranch); err != nil {
		return "", fmt.Errorf("发布分支: %w", err)
	}
	return head, nil
}

func (m *Manager) Remove(ctx context.Context, repositoryID, workItemID string) error {
	if err := validateID(repositoryID); err != nil {
		return err
	}
	if err := validateID(workItemID); err != nil {
		return err
	}
	cachePath := filepath.Join(m.cacheRoot, repositoryID, "repository.git")
	worktreePath := filepath.Join(m.worktreeRoot, workItemID)
	lockPath := filepath.Join(m.cacheRoot, repositoryID, ".lock")
	unlock, err := lock(lockPath)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := os.Stat(worktreePath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	_, err = runGit(ctx, cachePath, "", "worktree", "remove", "--force", worktreePath)
	return err
}

func revParse(ctx context.Context, path string) (string, error) {
	value, err := runGit(ctx, path, "", "rev-parse", "HEAD")
	return strings.TrimSpace(value), err
}

func runGit(ctx context.Context, dir, credential string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	env := append(gitEnvironment(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	cleanup := func() {}
	if credential != "" {
		ask, err := newAskPass(credential)
		if err != nil {
			return "", err
		}
		cleanup = ask.cleanup
		env = append(env, "GIT_ASKPASS="+ask.path, "TYRS_HAND_GIT_TOKEN="+credential)
	}
	defer cleanup()
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func gitEnvironment() []string {
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "TMPDIR": true, "LANG": true, "LC_ALL": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	}
	result := make([]string, 0, len(allowed))
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if allowed[key] {
			result = append(result, item)
		}
	}
	return result
}

type askPass struct {
	path    string
	cleanup func()
}

func newAskPass(token string) (askPass, error) {
	if token == "" {
		return askPass{}, errors.New("临时 Git credential 不能为空")
	}
	dir, err := os.MkdirTemp("", "tyrs-hand-askpass-")
	if err != nil {
		return askPass{}, err
	}
	path := filepath.Join(dir, "askpass.sh")
	script := "#!/bin/sh\ncase \"$1\" in *Username*) printf '%s\\n' 'x-access-token' ;; *) printf '%s\\n' \"$TYRS_HAND_GIT_TOKEN\" ;; esac\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return askPass{}, err
	}
	return askPass{path: path, cleanup: func() { _ = os.RemoveAll(dir) }}, nil
}

func lock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

func validateID(value string) error {
	if value == "" || value == "." || strings.ContainsAny(value, `/\\`) {
		return errors.New("资源 ID 无效")
	}
	return nil
}
