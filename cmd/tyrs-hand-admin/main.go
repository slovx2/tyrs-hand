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
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"github.com/slovx2/tyrs-hand/internal/codex"
	"github.com/slovx2/tyrs-hand/internal/config"
	"github.com/slovx2/tyrs-hand/internal/database"
	"github.com/slovx2/tyrs-hand/internal/discordintegration"
	"github.com/slovx2/tyrs-hand/internal/gitworkspace"
	"github.com/slovx2/tyrs-hand/internal/secrets"
	"github.com/slovx2/tyrs-hand/internal/security"
	"github.com/slovx2/tyrs-hand/internal/settings"
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
	case "discord-reconcile-posts":
		if len(os.Args) < 4 {
			usage()
		}
		fatal(reconcileDiscordPosts(ctx, db, cfg, os.Args[2], os.Args[3:]))
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
	cmd := exec.CommandContext(ctx, cfg.CodexBin,
		"-c", `cli_auth_credentials_store="file"`, "login", "--device-auth")
	cmd.Env = append(codexEnvironment(), "CODEX_HOME="+sharedHome, "HOME="+sharedHome)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	fatal(cmd.Run())
	fatal(settings.NewService(db, nil).SetChatGPTConfigured(ctx, true))
	audit(ctx, db, "settings.chatgpt.login.completed", "platform_setting", "agent.provider")
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

const discordPostCleanupConfirmation = "DELETE DUPLICATE DISCORD POSTS"

type discordPostRepair struct {
	requestID, operationKey string
	guildID, forumID        string
	routeKey, status        string
	payload                 json.RawMessage
	fingerprint             string
	attempt                 int
	createdAt               time.Time
}

type discordPostRepairGroup struct {
	guildID, forumID, fingerprint string
	repairs                       []discordPostRepair
}

func reconcileDiscordPosts(ctx context.Context, db *sql.DB, cfg config.Config,
	confirmation string, rawRequestIDs []string,
) error {
	if confirmation != discordPostCleanupConfirmation {
		return fmt.Errorf("确认文本必须精确等于 %q", discordPostCleanupConfirmation)
	}
	if len(cfg.MasterKey) != 32 {
		return errors.New("对账 Discord Post 必须配置主密钥")
	}
	box, err := security.NewSecretBox(cfg.MasterKey)
	if err != nil {
		return err
	}
	manager := discordintegration.NewManager(db, secrets.NewStore(db, box), "")
	settings, err := manager.Settings(ctx)
	if err != nil {
		return err
	}
	if settings.BotUserID == "" {
		return errors.New("对账 Discord Post 必须配置 Bot User ID")
	}
	token, err := manager.BotToken(ctx)
	if err != nil {
		return err
	}
	remote := discordintegration.NewDisgoRemote(token, "", nil)
	defer remote.Close(context.Background())

	repairs := make([]discordPostRepair, 0, len(rawRequestIDs))
	for _, rawID := range rawRequestIDs {
		requestID, err := uuid.Parse(rawID)
		if err != nil {
			return fmt.Errorf("desktop request ID 无效: %s", rawID)
		}
		var repair discordPostRepair
		repair.requestID = requestID.String()
		err = db.QueryRowContext(ctx, `SELECT outbox.operation_key, forum.guild_id,
			resource.discord_id, COALESCE(outbox.inflight_route_key,outbox.route_key),
			COALESCE(outbox.inflight_payload,outbox.payload),
			outbox.status, outbox.attempt_count, outbox.created_at
			FROM desktop_thread_requests request
			JOIN integration_outbox outbox
				ON outbox.operation_key='desktop-thread-post:' || request.id::text
			JOIN discord_forums forum ON forum.id=request.forum_id
			JOIN discord_resources resource ON resource.id=forum.resource_id
			WHERE request.id=$1 AND outbox.integration='discord'
				AND outbox.status IN ('ambiguous','completed')`, requestID).
			Scan(&repair.operationKey, &repair.guildID, &repair.forumID,
				&repair.routeKey, &repair.payload, &repair.status, &repair.attempt,
				&repair.createdAt)
		if err != nil {
			return fmt.Errorf("读取待对账 Desktop Post %s: %w", requestID, err)
		}
		if repair.guildID != settings.GuildID || repair.routeKey == "" {
			return fmt.Errorf("desktop Post %s 的 Guild 或请求快照无效", requestID)
		}
		repair.fingerprint, err = discordintegration.ForumPostRequestFingerprint(
			repair.payload, settings.BotUserID)
		if err != nil {
			return fmt.Errorf("计算 Desktop Post %s 请求指纹: %w", requestID, err)
		}
		repairs = append(repairs, repair)
	}
	if err := validateDiscordPostFingerprints(ctx, db, settings.BotUserID, repairs); err != nil {
		return err
	}

	groupsByKey := make(map[string]*discordPostRepairGroup, len(repairs))
	for _, repair := range repairs {
		key := repair.guildID + "\x00" + repair.forumID + "\x00" + repair.fingerprint
		group := groupsByKey[key]
		if group == nil {
			group = &discordPostRepairGroup{guildID: repair.guildID,
				forumID: repair.forumID, fingerprint: repair.fingerprint}
			groupsByKey[key] = group
		}
		group.repairs = append(group.repairs, repair)
	}
	groups := make([]*discordPostRepairGroup, 0, len(groupsByKey))
	for _, group := range groupsByKey {
		sort.Slice(group.repairs, func(i, j int) bool {
			if group.repairs[i].createdAt.Equal(group.repairs[j].createdAt) {
				return group.repairs[i].requestID < group.repairs[j].requestID
			}
			return group.repairs[i].createdAt.Before(group.repairs[j].createdAt)
		})
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].repairs[0].createdAt.Before(groups[j].repairs[0].createdAt)
	})

	receiptsByForum := make(map[string][]discordintegration.ForumPostReceipt)
	store := discordintegration.NewSQLoutbox(db)
	for _, group := range groups {
		key := group.guildID + ":" + group.forumID
		receipts, ok := receiptsByForum[key]
		if !ok {
			receipts, err = remote.ActiveForumPostReceipts(ctx, group.guildID, group.forumID)
			if err != nil {
				return fmt.Errorf("读取 Discord Forum Post 回执: %w", err)
			}
			receiptsByForum[key] = receipts
		}
		matchedByThread := make(map[string]discordintegration.ForumPostReceipt)
		for _, receipt := range receipts {
			if receipt.Fingerprint != group.fingerprint ||
				receipt.CreatedAt.Before(group.repairs[0].createdAt.Add(-time.Minute)) {
				continue
			}
			current, exists := matchedByThread[receipt.ThreadID]
			if !exists || receipt.CreatedAt.Before(current.CreatedAt) {
				matchedByThread[receipt.ThreadID] = receipt
			}
		}
		matched := make([]discordintegration.ForumPostReceipt, 0, len(matchedByThread))
		for _, receipt := range matchedByThread {
			matched = append(matched, receipt)
		}
		sort.Slice(matched, func(i, j int) bool {
			if matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
				return matched[i].ThreadID < matched[j].ThreadID
			}
			return matched[i].CreatedAt.Before(matched[j].CreatedAt)
		})
		if len(matched) < len(group.repairs) {
			return fmt.Errorf("desktop Post 对账组 %s 的精确匹配不足，拒绝删除",
				group.repairs[0].requestID)
		}
		totalAttempts := 0
		for _, repair := range group.repairs {
			totalAttempts += repair.attempt
		}
		if len(matched) > totalAttempts {
			return fmt.Errorf("desktop Post 对账组 %s 的匹配数超过投递次数，拒绝删除",
				group.repairs[0].requestID)
		}
		for index, repair := range group.repairs {
			if repair.status != "ambiguous" {
				continue
			}
			keep := matched[index]
			response, err := json.Marshal(map[string]string{
				"threadId": keep.ThreadID, "messageId": keep.MessageID,
			})
			if err != nil {
				return err
			}
			item, err := store.ResolveAmbiguousDelivery(ctx, repair.operationKey, response)
			if err != nil {
				return fmt.Errorf("恢复 Desktop Post %s 的远端回执: %w", repair.requestID, err)
			}
			if err := store.Apply(ctx, *item); err != nil {
				return fmt.Errorf("应用 Desktop Post %s 的本地状态: %w", repair.requestID, err)
			}
		}
		deleted := 0
		for _, duplicate := range matched[len(group.repairs):] {
			if err := remote.DeleteChannel(ctx, duplicate.ThreadID); err != nil {
				return fmt.Errorf("删除 Desktop Post 对账组 %s 的重复 Thread: %w",
					group.repairs[0].requestID, err)
			}
			deleted++
		}
		_, err = db.ExecContext(ctx, `INSERT INTO audit_logs
			(action,resource_type,resource_id,metadata)
			VALUES ('discord.outbox.reconcile','desktop_thread_request_group',$1,
				jsonb_build_object('requests',$2::integer,'matched',$3::integer,
					'deleted',$4::integer))`, group.repairs[0].requestID,
			len(group.repairs), len(matched), deleted)
		if err != nil {
			return err
		}
		fmt.Printf("Desktop Post 对账组已完成：请求 %d，保留 %d，删除 %d。\n",
			len(group.repairs), len(group.repairs), deleted)
	}
	return nil
}

func validateDiscordPostFingerprints(ctx context.Context, db *sql.DB, botUserID string,
	repairs []discordPostRepair,
) error {
	targets := make(map[string]string, len(repairs))
	selected := make(map[string]bool, len(repairs))
	targetRoutes := make(map[string]bool, len(repairs))
	for _, repair := range repairs {
		key := repair.routeKey + "\x00" + repair.fingerprint
		if _, exists := targets[key]; !exists {
			targets[key] = repair.requestID
		}
		selected[repair.operationKey] = true
		targetRoutes[repair.routeKey] = true
	}
	rows, err := db.QueryContext(ctx, `SELECT operation_key,
		COALESCE(inflight_route_key,route_key), COALESCE(inflight_payload,payload)
		FROM integration_outbox WHERE integration='discord'
			AND COALESCE(inflight_operation_type,operation_type)='forum.post.create'`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var operationKey, routeKey string
		var payload json.RawMessage
		if err := rows.Scan(&operationKey, &routeKey, &payload); err != nil {
			return err
		}
		if selected[operationKey] || !targetRoutes[routeKey] {
			continue
		}
		fingerprint, err := discordintegration.ForumPostRequestFingerprint(payload, botUserID)
		if err != nil {
			return fmt.Errorf("计算已有 Discord Forum Post 请求指纹: %w", err)
		}
		if requestID, exists := targets[routeKey+"\x00"+fingerprint]; exists {
			return fmt.Errorf("desktop Post %s 与另一条 Outbox 的远端语义指纹相同，拒绝删除",
				requestID)
		}
	}
	return rows.Err()
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
  tyrs-hand-admin discord-reconcile-posts "DELETE DUPLICATE DISCORD POSTS" <desktop-request-id>...
  tyrs-hand-admin gc`))
	os.Exit(2)
}

func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
