package gitworkspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/slovx2/tyrs-hand/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestEnsureAndPublish(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	require.NoError(t, os.MkdirAll(seed, 0o750))
	run(t, root, "git", "init", "--bare", remote)
	run(t, seed, "git", "init", "-b", "main")
	run(t, seed, "git", "config", "user.name", "Test")
	run(t, seed, "git", "config", "user.email", "test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(seed, "README.md"), []byte("hello\n"), 0o600))
	run(t, seed, "git", "add", "README.md")
	run(t, seed, "git", "commit", "-m", "initial")
	run(t, seed, "git", "remote", "add", "origin", remote)
	run(t, seed, "git", "push", "origin", "main")
	run(t, root, "git", "--git-dir", remote, "symbolic-ref", "HEAD", "refs/heads/main")

	manager := NewManager(filepath.Join(root, "cache"), filepath.Join(root, "worktrees"))
	workspace, err := manager.Ensure(ctx, ports.WorkspaceSpec{
		RepositoryID: "repo-1", WorkItemID: "item-1", CloneURL: remote,
		BaseRef: "refs/remotes/origin/main", Branch: "tyrs-hand/issue-1-test",
	}, "")
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(workspace.WorktreePath, "README.md"))
	status, err := manager.Status(ctx, workspace.WorktreePath)
	require.NoError(t, err)
	require.Contains(t, status, "tyrs-hand/issue-1-test")
	reused, err := manager.Ensure(ctx, ports.WorkspaceSpec{
		RepositoryID: "repo-1", WorkItemID: "item-1", CloneURL: remote,
		BaseRef: "refs/remotes/origin/main", Branch: "tyrs-hand/issue-1-test",
	}, "")
	require.NoError(t, err)
	require.Equal(t, workspace.WorktreePath, reused.WorktreePath)
	require.NoError(t, os.WriteFile(filepath.Join(seed, "pull-request.txt"), []byte("pull request\n"), 0o600))
	run(t, seed, "git", "add", "pull-request.txt")
	run(t, seed, "git", "commit", "-m", "pull request")
	run(t, seed, "git", "push", "origin", "HEAD:refs/pull/8/head")
	pullWorkspace, err := manager.Ensure(ctx, ports.WorkspaceSpec{
		RepositoryID: "repo-1", WorkItemID: "pull-8", CloneURL: remote,
		BaseRef: "refs/remotes/pull/8", Branch: "tyrs-hand/pull-8",
	}, "")
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(pullWorkspace.WorktreePath, "pull-request.txt"))

	require.NoError(t, os.WriteFile(filepath.Join(workspace.WorktreePath, "change.txt"), []byte("change\n"), 0o600))
	commitSHA, err := manager.Commit(ctx, workspace.WorktreePath, "feat: add change")
	require.NoError(t, err)
	require.NotEmpty(t, commitSHA)
	_, err = manager.Commit(ctx, workspace.WorktreePath, "empty")
	require.Error(t, err)
	sha, err := manager.Publish(ctx, workspace.WorktreePath, "tyrs-hand/issue-1-test", "")
	require.NoError(t, err)
	require.NotEmpty(t, sha)
	run(t, root, "git", "--git-dir", remote, "rev-parse", "refs/heads/tyrs-hand/issue-1-test")

	var group sync.WaitGroup
	errors := make(chan error, 2)
	for _, item := range []string{"item-2", "item-3"} {
		group.Add(1)
		go func(item string) {
			defer group.Done()
			_, err := manager.Ensure(ctx, ports.WorkspaceSpec{
				RepositoryID: "repo-1", WorkItemID: item, CloneURL: remote,
				BaseRef: "refs/remotes/origin/main", Branch: "tyrs-hand/" + item,
			}, "")
			errors <- err
		}(item)
	}
	group.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}

	corruptPath := filepath.Join(root, "worktrees", "item-corrupt")
	require.NoError(t, os.MkdirAll(corruptPath, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(corruptPath, "untrusted.txt"), []byte("preserve\n"), 0o600))
	rebuilt, err := manager.Ensure(ctx, ports.WorkspaceSpec{
		RepositoryID: "repo-1", WorkItemID: "item-corrupt", CloneURL: remote,
		BaseRef: "refs/remotes/origin/main", Branch: "tyrs-hand/item-corrupt",
	}, "")
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(rebuilt.WorktreePath, "README.md"))
	quarantined, err := filepath.Glob(corruptPath + ".quarantine-*")
	require.NoError(t, err)
	require.Len(t, quarantined, 1)
	require.FileExists(t, filepath.Join(quarantined[0], "untrusted.txt"))

	_, err = manager.Publish(ctx, workspace.WorktreePath, "-invalid", "")
	require.Error(t, err)
	require.NoError(t, manager.Remove(ctx, "repo-1", "item-1"))
	require.NoError(t, manager.Remove(ctx, "repo-1", "item-1"))
	require.NoDirExists(t, workspace.WorktreePath)
}

func TestWorkspaceRejectsInvalidSpecs(t *testing.T) {
	manager := NewManager(t.TempDir(), t.TempDir())
	_, err := manager.Ensure(context.Background(), ports.WorkspaceSpec{RepositoryID: "../escape", WorkItemID: "item"}, "")
	require.Error(t, err)
	_, err = manager.Ensure(context.Background(), ports.WorkspaceSpec{RepositoryID: "repo", WorkItemID: "../escape"}, "")
	require.Error(t, err)
	require.Error(t, manager.Remove(context.Background(), "../escape", "item"))
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}
