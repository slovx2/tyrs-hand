package devcontainer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (m *Manager) provision(ctx context.Context, item *workspace, credential string,
	processEnvironment []string,
) error {
	firstProvision := item.Environment.ContainerID == ""
	if m.db != nil {
		_, _ = m.db.ExecContext(ctx, `UPDATE discord_development_environments
			SET status = 'building', error = NULL, updated_at = now() WHERE id = $1`, item.Environment.ID)
	}
	imageRef := strings.TrimSpace(item.Environment.ImageRef)
	if imageRef == "" {
		return errorsNew("开发环境缺少官方开发镜像引用")
	}
	identity, err := m.inspectDevelopmentImage(ctx, imageRef)
	if err != nil {
		return err
	}
	imageID, runtimeUser := identity.ImageID, identity.User
	uid, gid, home := identity.UID, identity.GID, identity.Home
	if item.Environment.RuntimeUID != 0 &&
		(item.Environment.RuntimeUser != runtimeUser || item.Environment.RuntimeUID != uid ||
			item.Environment.RuntimeGID != gid || item.Environment.RuntimeHome != home) {
		return errorsNew("重建镜像改变了 USER、UID/GID 或 Home 路径；请恢复原身份或删除最后一个 Forum 后重建")
	}
	if err := m.ensureDockerResource(ctx, "volume", item.Environment.DataVolume); err != nil {
		return err
	}
	if err := m.ensureDockerResource(ctx, "volume", item.Environment.HomeVolume); err != nil {
		return err
	}
	if err := m.ensureDockerResource(ctx, "network", item.Environment.Network); err != nil {
		return err
	}
	runtimeDir := filepath.Join(m.developmentRuntimeDir, item.Environment.ID.String())
	if err := os.MkdirAll(runtimeDir, 0o770); err != nil {
		return fmt.Errorf("创建环境运行目录: %w", err)
	}
	hostRuntimeDir := filepath.Join(m.developmentRuntimeHostDir, item.Environment.ID.String())
	candidateName := item.Environment.ContainerName + "-candidate-" + time.Now().UTC().Format("20060102150405")
	_, _ = m.docker(ctx, "rm", "--force", candidateName)
	createArguments := []string{"create", "--name", candidateName, "--restart", "unless-stopped",
		"--label", "com.tyrs-hand.development-environment=" + item.Environment.ID.String(),
		"--network", item.Environment.Network, "--volume", item.Environment.DataVolume + ":" + containerRoot,
		"--volume", item.Environment.HomeVolume + ":" + home,
		"--mount", "type=bind,source=" + hostRuntimeDir + ",target=" + containerRunDir,
		"--add-host", "host.docker.internal:host-gateway"}
	if m.sshEnabled {
		createArguments = append(createArguments, "--mount", "type=bind,source="+
			m.sshAgentHostDir+",target="+m.sshAgentDir)
	}
	if m.browserEnabled {
		createArguments = append(createArguments, "--mount", "type=bind,source="+
			m.browserFilesHostRoot+",target="+m.browserFilesRoot)
	}
	createArguments = append(createArguments, "--entrypoint", "/bin/sh", imageRef,
		"-c", "while :; do sleep 3600; done")
	containerID, err := m.docker(ctx, createArguments...)
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
	if err := m.configureRemoteDaemons(ctx, candidateName, RemoteOperation{
		EnvironmentID: item.Environment.ID, RuntimeUser: runtimeUser, RuntimeUID: uid,
		RuntimeGID: gid, RuntimeHome: home, ProcessEnvironment: processEnvironment,
	}); err != nil {
		return err
	}
	if _, err := m.docker(ctx, "exec", "--user", fmt.Sprintf("%d:%d", uid, gid),
		"--env", "HOME="+home, candidateName, "codex", "--version"); err != nil {
		return fmt.Errorf("验证开发容器内 Codex 运行时: %w", err)
	}
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
		status = 'running', image_ref = $2, image_id = $3,
		container_id = $4, runtime_user = $5, runtime_uid = $6, runtime_gid = $7, runtime_home = $8,
		error = NULL, last_used_at = now(), updated_at = now() WHERE id = $1`,
			item.Environment.ID, imageRef, imageID, containerID, runtimeUser, uid, gid, home)
	}
	if err != nil {
		_, _ = m.docker(context.Background(), "rm", "--force", item.Environment.ContainerName)
		m.restorePreviousContainer(backupName, item.Environment.ContainerName)
		return err
	}
	item.Environment.Status = "running"
	item.Environment.ImageRef, item.Environment.ImageID = imageRef, imageID
	item.Environment.ContainerID, item.Environment.RuntimeUser = containerID, runtimeUser
	item.Environment.RuntimeUID, item.Environment.RuntimeGID, item.Environment.RuntimeHome = uid, gid, home
	if backupName != "" {
		_, _ = m.docker(context.Background(), "rm", "--force", backupName)
	}
	return nil
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

type developmentImageIdentity struct {
	ImageID string
	User    string
	UID     int64
	GID     int64
	Home    string
}

func (m *Manager) inspectDevelopmentImage(ctx context.Context,
	imageRef string,
) (developmentImageIdentity, error) {
	if _, err := m.docker(ctx, "image", "inspect", imageRef); err != nil {
		if _, pullErr := m.docker(ctx, "pull", imageRef); pullErr != nil {
			return developmentImageIdentity{}, fmt.Errorf("拉取开发镜像: %w", pullErr)
		}
	}
	imageID, err := m.docker(ctx, "image", "inspect", "--format", "{{.Id}}", imageRef)
	if err != nil {
		return developmentImageIdentity{}, err
	}
	contract, err := m.docker(ctx, "image", "inspect", "--format",
		"{{index .Config.Labels \"ai.tyrs-hand.development.contract\"}}", imageRef)
	if err != nil || strings.TrimSpace(contract) != "1" {
		return developmentImageIdentity{}, errorsNew("开发镜像不符合 Tyrs Hand 开发容器契约 1")
	}
	configUser, err := m.docker(ctx, "image", "inspect", "--format", "{{.Config.User}}", imageRef)
	if err != nil {
		return developmentImageIdentity{}, err
	}
	configUser = strings.TrimSpace(configUser)
	lookup := strings.Split(configUser, ":")[0]
	if lookup == "" || lookup == "0" || lookup == "root" {
		return developmentImageIdentity{}, errorsNew("开发镜像必须声明非 root USER")
	}
	identity, err := m.docker(ctx, "run", "--rm", "--user", "0:0", "--entrypoint", "/bin/sh",
		"--env", "TYRS_RUNTIME_USER="+lookup, imageRef, "-c",
		`command -v git >/dev/null && command -v getent >/dev/null && command -v sleep >/dev/null || exit 20
command -v ssh >/dev/null && command -v scp >/dev/null && command -v sftp >/dev/null || exit 23
test -x /usr/sbin/sshd && command -v ssh-keygen >/dev/null && test -x /usr/lib/openssh/sftp-server || exit 24
command -v codex >/dev/null && command -v apply_patch >/dev/null && command -v tyrs-hand-dev >/dev/null || exit 25
entry=$(getent passwd "$TYRS_RUNTIME_USER") || exit 21
name=$(printf '%s' "$entry" | cut -d: -f1)
uid=$(printf '%s' "$entry" | cut -d: -f3)
gid=$(printf '%s' "$entry" | cut -d: -f4)
home=$(printf '%s' "$entry" | cut -d: -f6)
test -n "$name" -a -n "$uid" -a "$uid" != 0 -a -n "$gid" -a -n "$home" || exit 22
printf '%s:%s:%s:%s' "$name" "$uid" "$gid" "$home"`)
	if err != nil {
		return developmentImageIdentity{}, fmt.Errorf("验证开发镜像运行用户、Git 和基础命令: %w", err)
	}
	parts := strings.SplitN(strings.TrimSpace(identity), ":", 4)
	if len(parts) != 4 || parts[0] == "" {
		return developmentImageIdentity{}, errorsNew("开发镜像 USER 身份格式无效")
	}
	uid, gid, home, err := parseIdentity(strings.Join(parts[1:], ":"))
	if err != nil || home == "/" {
		return developmentImageIdentity{}, errorsNew("开发镜像 USER 必须有独立 Home 目录")
	}
	return developmentImageIdentity{ImageID: strings.TrimSpace(imageID), User: parts[0],
		UID: uid, GID: gid, Home: home}, nil
}

func errorsNew(message string) error { return fmt.Errorf("%s", message) }
