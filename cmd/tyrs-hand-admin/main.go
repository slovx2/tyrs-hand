package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pquerna/otp/totp"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/gitworkspace"
	"github.com/slovx2/tyrs-hand/internal/security"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cfg, err := config.Load()
	fatal(err)
	ctx := context.Background()
	db, err := database.Open(ctx, cfg.DatabaseURL)
	fatal(err)
	defer func() { _ = db.Close() }()
	switch os.Args[1] {
	case "migrate":
		fatal(database.Migrate(ctx, db))
		fmt.Println("数据库迁移完成。")
	case "check-control":
		fatal(diagnoseControl(ctx, db))
	case "check-worker":
		fatal(diagnoseWorker(ctx, db, cfg))
	case "reset-password":
		requireArgs(4)
		resetPassword(ctx, db, os.Args[2], os.Args[3])
	case "recover-password":
		requireArgs(5)
		recoverPassword(ctx, db, os.Args[2], os.Args[3], os.Args[4])
	case "reset-totp":
		requireArgs(3)
		resetTOTP(ctx, db, cfg, os.Args[2])
	case "rotate-master-key":
		requireArgs(3)
		rotateMasterKey(ctx, db, cfg.MasterKey, os.Args[2])
	case "codex-login":
		requireArgs(2)
		codexLogin(ctx, db, cfg)
	case "gc":
		requireArgs(2)
		garbageCollect(ctx, db, cfg)
	default:
		usage()
	}
}

func resetPassword(ctx context.Context, db *sql.DB, username, password string) {
	hash, err := security.HashPassword(password)
	fatal(err)
	result, err := db.ExecContext(ctx, "UPDATE administrators SET password_hash = $2, updated_at = now() WHERE username = $1", username, hash)
	requireUpdated(result, err, username)
	audit(ctx, db, "admin.password.reset", "administrator", username)
	fmt.Println("管理员密码已经重置。")
}

func recoverPassword(ctx context.Context, db *sql.DB, username, recoveryCode, password string) {
	var raw []byte
	fatal(db.QueryRowContext(ctx, "SELECT recovery_codes_hash FROM administrators WHERE username = $1", username).Scan(&raw))
	var hashes []string
	fatal(json.Unmarshal(raw, &hashes))
	target := security.Digest(recoveryCode)
	remaining := make([]string, 0, len(hashes))
	found := false
	for _, hash := range hashes {
		if hash == target {
			found = true
			continue
		}
		remaining = append(remaining, hash)
	}
	if !found {
		fatal(errors.New("恢复码无效或已经使用"))
	}
	passwordHash, err := security.HashPassword(password)
	fatal(err)
	encoded, err := json.Marshal(remaining)
	fatal(err)
	_, err = db.ExecContext(ctx, `UPDATE administrators SET password_hash = $2, recovery_codes_hash = $3, updated_at = now() WHERE username = $1`, username, passwordHash, encoded)
	fatal(err)
	audit(ctx, db, "admin.password.recover", "administrator", username)
	fmt.Println("管理员密码已经通过一次性恢复码重置。")
}

func resetTOTP(ctx context.Context, db *sql.DB, cfg config.Config, username string) {
	if len(cfg.MasterKey) != 32 {
		fatal(errors.New("重置 TOTP 必须配置主密钥"))
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "tyrs-hand", AccountName: username})
	fatal(err)
	box, err := security.NewSecretBox(cfg.MasterKey)
	fatal(err)
	nonce, ciphertext, err := box.Encrypt([]byte(key.Secret()), "administrator.totp")
	fatal(err)
	result, err := db.ExecContext(ctx, "UPDATE administrators SET totp_secret_ciphertext = $2, updated_at = now() WHERE username = $1", username, append(nonce, ciphertext...))
	requireUpdated(result, err, username)
	audit(ctx, db, "admin.totp.reset", "administrator", username)
	fmt.Printf("TOTP Secret: %s\nProvisioning URI: %s\n", key.Secret(), key.URL())
}

func rotateMasterKey(ctx context.Context, db *sql.DB, oldKey []byte, newKeyFile string) {
	if len(oldKey) != 32 {
		fatal(errors.New("轮换前必须通过环境配置当前主密钥"))
	}
	raw, err := os.ReadFile(newKeyFile)
	fatal(err)
	newKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(newKey) != 32 {
		fatal(errors.New("新主密钥文件必须包含 32 字节随机值的 base64 编码"))
	}
	oldBox, err := security.NewSecretBox(oldKey)
	fatal(err)
	newBox, err := security.NewSecretBox(newKey)
	fatal(err)
	tx, err := db.BeginTx(ctx, nil)
	fatal(err)
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, "SELECT id, secret_key, nonce, ciphertext FROM encrypted_secrets FOR UPDATE")
	fatal(err)
	type rotatedSecret struct {
		id, key           string
		nonce, ciphertext []byte
	}
	var values []rotatedSecret
	for rows.Next() {
		var value rotatedSecret
		fatal(rows.Scan(&value.id, &value.key, &value.nonce, &value.ciphertext))
		plain, err := oldBox.Decrypt(value.nonce, value.ciphertext, value.key)
		fatal(err)
		value.nonce, value.ciphertext, err = newBox.Encrypt(plain, value.key)
		fatal(err)
		values = append(values, value)
	}
	fatal(rows.Err())
	fatal(rows.Close())
	for _, value := range values {
		_, err := tx.ExecContext(ctx, `UPDATE encrypted_secrets SET nonce = $2, ciphertext = $3, key_version = key_version + 1, updated_at = now() WHERE id = $1`, value.id, value.nonce, value.ciphertext)
		fatal(err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_logs(action, resource_type, metadata) VALUES ('master_key.rotate', 'encrypted_secret', jsonb_build_object('count', $1::integer))`, len(values))
	fatal(err)
	fatal(tx.Commit())
	fmt.Printf("已轮换 %d 个 Secret。请先更新运行环境中的主密钥，再重启服务。\n", len(values))
}

func codexLogin(ctx context.Context, db *sql.DB, cfg config.Config) {
	sharedHome := filepath.Join(cfg.CodexHomeRoot, "shared")
	fatal(os.MkdirAll(sharedHome, 0o700))
	cmd := exec.CommandContext(ctx, cfg.CodexBin, "login", "--device-auth")
	cmd.Env = append(codexEnvironment(), "CODEX_HOME="+sharedHome, "HOME="+sharedHome)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	fatal(cmd.Run())
	_, err := db.ExecContext(ctx, `
		INSERT INTO platform_settings(setting_key, value)
		VALUES ('agent.provider', '{"providerType":"device-code","configured":true,"configSignature":"device-code"}'::jsonb)
		ON CONFLICT(setting_key) DO UPDATE SET value = jsonb_set(platform_settings.value, '{configured}', 'true'::jsonb),
			version = platform_settings.version + 1, updated_at = now()`)
	fatal(err)
	audit(ctx, db, "agent_provider.device_login", "platform_setting", "agent.provider")
	fmt.Println("共享 Codex 账号登录完成。")
}

func diagnoseControl(ctx context.Context, db *sql.DB) error {
	if err := database.CheckMigrations(ctx, db); err != nil {
		return err
	}
	fmt.Println("数据库迁移状态正常。")
	return nil
}

func diagnoseWorker(ctx context.Context, db *sql.DB, cfg config.Config) error {
	if err := diagnoseControl(ctx, db); err != nil {
		return err
	}
	if err := codex.ValidateVersion(ctx, cfg.CodexBin); err != nil {
		return err
	}
	if cfg.EnableDevelopmentContainers && (cfg.WorkerRole == "discord" || cfg.WorkerRole == "all") {
		docker := exec.CommandContext(ctx, "/usr/local/libexec/tyrs-hand/docker", "version")
		docker.Env = append(codexEnvironment(), "DOCKER_HOST=unix:///var/run/docker.sock")
		if output, err := docker.CombinedOutput(); err != nil {
			return fmt.Errorf("检查开发容器 Docker Daemon: %w: %s", err, strings.TrimSpace(string(output)))
		}
	}
	for _, path := range []string{cfg.WorkerDataRoot, cfg.RepoCacheRoot, cfg.WorktreeRoot,
		cfg.CodexHomeRoot} {
		if err := os.MkdirAll(path, 0o750); err != nil {
			return fmt.Errorf("检查目录 %s: %w", path, err)
		}
	}
	fmt.Println("Worker 运行时和本地目录均正常。")
	return nil
}

func codexEnvironment() []string {
	allowed := map[string]bool{
		"PATH": true, "LANG": true, "LC_ALL": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true,
		"SSL_CERT_FILE": true, "CODEX_CA_CERTIFICATE": true,
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

func garbageCollect(ctx context.Context, db *sql.DB, cfg config.Config) {
	manager := gitworkspace.NewManager(cfg.RepoCacheRoot, cfg.WorktreeRoot)
	rows, err := db.QueryContext(ctx, `
		SELECT r.id, w.id FROM work_items w JOIN repositories r ON r.id = w.repository_id
		JOIN worktrees wt ON wt.work_item_id = w.id
		WHERE w.closed_at < now() - interval '7 days'`)
	fatal(err)
	removed := 0
	for rows.Next() {
		var repositoryID, workItemID string
		fatal(rows.Scan(&repositoryID, &workItemID))
		if err := manager.Remove(ctx, repositoryID, workItemID); err != nil {
			fmt.Fprintf(os.Stderr, "清理 Worktree %s 失败: %v\n", workItemID, err)
			continue
		}
		_, err := db.ExecContext(ctx, "DELETE FROM worktrees WHERE work_item_id = $1", workItemID)
		fatal(err)
		removed++
	}
	fatal(rows.Close())
	sessions, err := db.ExecContext(ctx, "DELETE FROM admin_sessions WHERE expires_at < now()")
	fatal(err)
	sessionCount, _ := sessions.RowsAffected()
	cacheCount := collectRepoCaches(ctx, db, cfg)
	audit(ctx, db, "gc.complete", "system", "")
	fmt.Printf("GC 完成：Worktree %d，Repo Cache %d，Session %d。\n", removed, cacheCount, sessionCount)
}

func collectRepoCaches(ctx context.Context, db *sql.DB, cfg config.Config) int {
	rows, err := db.QueryContext(ctx, `
		SELECT rc.id, rc.path FROM repo_caches rc
		WHERE NOT EXISTS (SELECT 1 FROM worktrees wt WHERE wt.repo_cache_id = rc.id)
		ORDER BY rc.last_used_at DESC`)
	fatal(err)
	defer func() { _ = rows.Close() }()
	type cache struct {
		id, path string
		size     int64
	}
	var caches []cache
	var total int64
	for rows.Next() {
		var item cache
		fatal(rows.Scan(&item.id, &item.path))
		item.size, err = directorySize(item.path)
		fatal(err)
		total += item.size
		caches = append(caches, item)
		_, _ = db.ExecContext(ctx, "UPDATE repo_caches SET size_bytes = $2 WHERE id = $1", item.id, item.size)
	}
	fatal(rows.Err())
	removed := 0
	for index := len(caches) - 1; index >= 0 && total > cfg.RepoCacheMaxBytes; index-- {
		item := caches[index]
		root, err := filepath.Abs(cfg.RepoCacheRoot)
		fatal(err)
		path, err := filepath.Abs(item.path)
		fatal(err)
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			continue
		}
		fatal(os.RemoveAll(filepath.Dir(path)))
		_, err = db.ExecContext(ctx, "DELETE FROM repo_caches WHERE id = $1", item.id)
		fatal(err)
		total -= item.size
		removed++
	}
	return removed
}

func directorySize(root string) (int64, error) {
	var size int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if info.Mode().IsRegular() {
			size += info.Size()
		}
		return nil
	})
	return size, err
}

func audit(ctx context.Context, db *sql.DB, action, resourceType, resourceID string) {
	_, err := db.ExecContext(ctx, `INSERT INTO audit_logs(action, resource_type, resource_id) VALUES ($1, $2, NULLIF($3, ''))`, action, resourceType, resourceID)
	fatal(err)
}

func requireUpdated(result sql.Result, err error, username string) {
	fatal(err)
	count, err := result.RowsAffected()
	fatal(err)
	if count != 1 {
		fatal(fmt.Errorf("管理员 %s 不存在", username))
	}
}

func requireArgs(count int) {
	if len(os.Args) != count {
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, strings.TrimSpace(`
Usage:
  tyrs-hand-admin migrate
  tyrs-hand-admin check-control
  tyrs-hand-admin check-worker
  tyrs-hand-admin reset-password <username> <new-password>
  tyrs-hand-admin recover-password <username> <recovery-code> <new-password>
  tyrs-hand-admin reset-totp <username>
  tyrs-hand-admin rotate-master-key <new-master-key-file>
  tyrs-hand-admin codex-login
  tyrs-hand-admin gc`))
	os.Exit(2)
}

func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
