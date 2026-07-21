package devcontainer

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var remoteBuildLock sync.Mutex

func (m *Manager) provision(ctx context.Context, item *workspace, credential string) error {
	buildLock, err := m.acquireBuildLock(ctx)
	if err != nil {
		return err
	}
	defer m.releaseBuildLock(buildLock)

	firstProvision := item.Environment.ContainerID == ""
	if m.db != nil {
		_, _ = m.db.ExecContext(ctx, `UPDATE discord_development_environments
			SET status = 'building', error = NULL, updated_at = now() WHERE id = $1`, item.Environment.ID)
	}
	checkout, cleanup, err := m.checkoutRepository(ctx, item.Environment.BuildCloneURL,
		item.Environment.BuildDefaultRef, "", credential)
	if err != nil {
		return err
	}
	defer cleanup()
	dockerfile := filepath.Join(checkout, ".devcontainer", "Dockerfile")
	if info, err := os.Stat(dockerfile); err != nil || info.IsDir() {
		return errorsNew("仓库默认分支必须提供 .devcontainer/Dockerfile")
	}
	imageRef := "tyrs-hand-dev:" + strings.ReplaceAll(item.Environment.ID.String(), "-", "") +
		"-" + time.Now().UTC().Format("20060102150405")
	if _, err := m.docker(ctx, "build", "--pull", "--file", dockerfile, "--tag", imageRef, checkout); err != nil {
		return fmt.Errorf("构建开发镜像: %w", err)
	}
	keepImage := false
	defer func() {
		if !keepImage {
			_, _ = m.docker(context.Background(), "image", "rm", "--force", imageRef)
		}
	}()
	imageID, err := m.docker(ctx, "image", "inspect", "--format", "{{.Id}}", imageRef)
	if err != nil {
		return err
	}
	runtimeUser, err := m.docker(ctx, "image", "inspect", "--format", "{{.Config.User}}", imageRef)
	if err != nil {
		return err
	}
	runtimeUser = strings.TrimSpace(runtimeUser)
	lookup := strings.Split(runtimeUser, ":")[0]
	if lookup == "" || lookup == "0" || lookup == "root" {
		return errorsNew("开发镜像必须声明非 root USER")
	}
	identity, err := m.docker(ctx, "run", "--rm", "--user", "0:0", "--entrypoint", "/bin/sh",
		"--env", "TYRS_RUNTIME_USER="+lookup, imageRef, "-c",
		`command -v git >/dev/null && command -v getent >/dev/null && command -v sleep >/dev/null || exit 20
entry=$(getent passwd "$TYRS_RUNTIME_USER") || exit 21
uid=$(printf '%s' "$entry" | cut -d: -f3)
gid=$(printf '%s' "$entry" | cut -d: -f4)
home=$(printf '%s' "$entry" | cut -d: -f6)
test -n "$uid" -a "$uid" != 0 -a -n "$gid" -a -n "$home" || exit 22
printf '%s:%s:%s' "$uid" "$gid" "$home"`)
	if err != nil {
		return fmt.Errorf("验证开发镜像运行用户、Git 和基础命令: %w", err)
	}
	uid, gid, home, err := parseIdentity(identity)
	if err != nil || home == "/" {
		return errorsNew("开发镜像 USER 必须有独立 Home 目录")
	}
	if item.Environment.RuntimeUID != 0 &&
		(item.Environment.RuntimeUser != runtimeUser || item.Environment.RuntimeUID != uid ||
			item.Environment.RuntimeGID != gid || item.Environment.RuntimeHome != home) {
		return errorsNew("重建镜像改变了 USER、UID/GID 或 Home 路径；请恢复原身份或删除最后一个 Forum 后重建")
	}
	previousImage := item.Environment.ImageRef
	if err := m.ensureDockerResource(ctx, "volume", item.Environment.DataVolume); err != nil {
		return err
	}
	if err := m.ensureDockerResource(ctx, "volume", item.Environment.HomeVolume); err != nil {
		return err
	}
	if err := m.ensureDockerResource(ctx, "network", item.Environment.Network); err != nil {
		return err
	}
	candidateName := item.Environment.ContainerName + "-candidate-" + time.Now().UTC().Format("20060102150405")
	_, _ = m.docker(ctx, "rm", "--force", candidateName)
	containerID, err := m.docker(ctx, "create", "--name", candidateName,
		"--label", "com.tyrs-hand.development-environment="+item.Environment.ID.String(),
		"--network", item.Environment.Network, "--volume", item.Environment.DataVolume+":"+containerRoot,
		"--volume", item.Environment.HomeVolume+":"+home, "--entrypoint", "/bin/sh", imageRef,
		"-c", "while :; do sleep 3600; done")
	if err != nil {
		return fmt.Errorf("创建开发容器: %w", err)
	}
	candidateExists := true
	defer func() {
		if candidateExists {
			_, _ = m.docker(context.Background(), "rm", "--force", candidateName)
		}
	}()
	if _, err := m.docker(ctx, "start", candidateName); err != nil {
		return err
	}
	if err := m.installRuntime(ctx, candidateName, uid, gid); err != nil {
		return err
	}
	if _, err := m.docker(ctx, "exec", "--user", fmt.Sprintf("%d:%d", uid, gid),
		"--env", "HOME="+home, candidateName, runtimeRoot+"/bin/codex", "--version"); err != nil {
		return fmt.Errorf("验证开发容器内 Codex 运行时: %w", err)
	}
	sha, _ := m.runner.Run(ctx, nil, checkout, "git", "rev-parse", "HEAD")
	backupName := ""
	if !firstProvision {
		backupName = item.Environment.ContainerName + "-previous-" + time.Now().UTC().Format("20060102150405")
		if _, err := m.docker(ctx, "stop", "--time", "10", item.Environment.ContainerName); err != nil {
			return fmt.Errorf("停止旧开发容器: %w", err)
		}
		if _, err := m.docker(ctx, "rename", item.Environment.ContainerName, backupName); err != nil {
			_, _ = m.docker(context.Background(), "start", item.Environment.ContainerName)
			return fmt.Errorf("保留旧开发容器: %w", err)
		}
	}
	if _, err := m.docker(ctx, "rename", candidateName, item.Environment.ContainerName); err != nil {
		m.restorePreviousContainer(backupName, item.Environment.ContainerName)
		return fmt.Errorf("切换新开发容器: %w", err)
	}
	candidateExists = false
	if m.db != nil {
		_, err = m.db.ExecContext(ctx, `UPDATE discord_development_environments SET
		status = 'running', image_ref = $2, image_id = $3, build_source_sha = NULLIF($4, ''),
		container_id = $5, runtime_user = $6, runtime_uid = $7, runtime_gid = $8, runtime_home = $9,
		error = NULL, idle_at = NULL, last_used_at = now(), updated_at = now() WHERE id = $1`,
			item.Environment.ID, imageRef, imageID, sha, containerID, runtimeUser, uid, gid, home)
	}
	if err != nil {
		_, _ = m.docker(context.Background(), "rm", "--force", item.Environment.ContainerName)
		m.restorePreviousContainer(backupName, item.Environment.ContainerName)
		return err
	}
	keepImage = true
	item.Environment.Status = "running"
	item.Environment.ImageRef, item.Environment.ImageID = imageRef, imageID
	item.Environment.ContainerID, item.Environment.RuntimeUser = containerID, runtimeUser
	item.Environment.RuntimeUID, item.Environment.RuntimeGID, item.Environment.RuntimeHome = uid, gid, home
	if backupName != "" {
		_, _ = m.docker(context.Background(), "rm", "--force", backupName)
	}
	if previousImage != "" && previousImage != imageRef {
		_, _ = m.docker(context.Background(), "image", "rm", previousImage)
	}
	return nil
}

func (m *Manager) acquireBuildLock(ctx context.Context) (*sql.Conn, error) {
	if m.db == nil {
		return nil, nil
	}
	connection, err := m.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := connection.ExecContext(ctx,
		"SELECT pg_advisory_lock(hashtext('discord-development-build-global'))"); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return connection, nil
}

func (m *Manager) releaseBuildLock(connection *sql.Conn) {
	if m.db == nil {
		return
	}
	if connection == nil {
		return
	}
	_, _ = connection.ExecContext(context.Background(),
		"SELECT pg_advisory_unlock(hashtext('discord-development-build-global'))")
	_ = connection.Close()
}

func (m *Manager) restorePreviousContainer(backupName, containerName string) {
	if backupName == "" {
		return
	}
	_, _ = m.docker(context.Background(), "rename", backupName, containerName)
	_, _ = m.docker(context.Background(), "start", containerName)
}

func (m *Manager) cloneWorkspace(ctx context.Context, item *workspace, credential string) error {
	if m.db != nil {
		_, _ = m.db.ExecContext(ctx, `UPDATE discord_forum_workspaces
			SET status = 'cloning', error = NULL, updated_at = now() WHERE forum_id = $1`, item.ForumID)
	}
	checkout, cleanup, err := m.checkoutRepository(ctx, item.CloneURL, item.DefaultRef, item.Branch, credential)
	if err != nil {
		return err
	}
	defer cleanup()
	return m.copyCheckout(ctx, item, checkout)
}

func (m *Manager) checkoutRepository(ctx context.Context, cloneURL, defaultRef, branch, credential string) (string, func(), error) {
	root := filepath.Join(m.dataRoot, "tmp")
	if err := os.MkdirAll(root, 0o750); err != nil {
		return "", func() {}, err
	}
	directory, err := os.MkdirTemp(root, "development-checkout-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	askpass, askCleanup, err := createAskPass(credential)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	environment := []string{"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=" + askpass, "TYRS_GIT_TOKEN=" + credential}
	if _, err := m.runner.Run(ctx, environment, "", "git", "clone", "--branch", defaultRef,
		"--single-branch", "--", cloneURL, directory); err != nil {
		askCleanup()
		cleanup()
		return "", func() {}, err
	}
	askCleanup()
	if branch != "" {
		if _, err := m.runner.Run(ctx, nil, directory, "git", "checkout", "-b", branch); err != nil {
			cleanup()
			return "", func() {}, err
		}
	}
	return directory, cleanup, nil
}

func (m *Manager) copyCheckout(ctx context.Context, item *workspace, checkout string) error {
	target := filepath.ToSlash(filepath.Join(containerRoot, item.Relative))
	owner := fmt.Sprintf("%d:%d", item.Environment.RuntimeUID, item.Environment.RuntimeGID)
	if _, err := m.docker(ctx, "exec", "--user", "0:0", item.Environment.ContainerName,
		"mkdir", "-p", target); err != nil {
		return err
	}
	if _, err := m.docker(ctx, "cp", filepath.Clean(checkout)+"/.", item.Environment.ContainerName+":"+target); err != nil {
		return err
	}
	if _, err := m.docker(ctx, "exec", "--user", "0:0", item.Environment.ContainerName,
		"chown", "-R", owner, target); err != nil {
		return err
	}
	sha, _ := m.runner.Run(ctx, nil, checkout, "git", "rev-parse", "HEAD")
	if m.db == nil {
		item.Status = "ready"
		return nil
	}
	_, err := m.db.ExecContext(ctx, `UPDATE discord_forum_workspaces SET status = 'ready',
		base_sha = $2, head_sha = $2, dirty = false, error = NULL, last_used_at = now(), updated_at = now()
		WHERE forum_id = $1`, item.ForumID, sha)
	return err
}

func errorsNew(message string) error { return fmt.Errorf("%s", message) }
