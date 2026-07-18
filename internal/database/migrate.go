package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	version  string
	checksum string
	content  string
	nonTx    bool
}

func Migrate(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("获取迁移连接: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version text PRIMARY KEY,
			checksum char(64) NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("初始化迁移表: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock(hashtext('tyrs-hand-migrations'))"); err != nil {
		return fmt.Errorf("获取迁移锁: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock(hashtext('tyrs-hand-migrations'))")
	}()

	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, item := range migrations {
		applied, err := migrationApplied(ctx, conn, item)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		if item.nonTx {
			if err := applyNonTransactional(ctx, conn, item); err != nil {
				return err
			}
			continue
		}
		if err := applyTransactional(ctx, conn, item); err != nil {
			return err
		}
	}
	return nil
}

func CheckMigrations(ctx context.Context, db *sql.DB) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, item := range migrations {
		var checksum string
		err := db.QueryRowContext(ctx, "SELECT checksum FROM schema_migrations WHERE version = $1", item.version).Scan(&checksum)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("数据库缺少迁移 %s", item.version)
		}
		if err != nil {
			return fmt.Errorf("检查迁移 %s: %w", item.version, err)
		}
		if checksum != item.checksum {
			return fmt.Errorf("迁移 %s checksum 不一致", item.version)
		}
	}
	return nil
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("读取迁移文件: %w", err)
	}
	items := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		data, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("读取迁移 %s: %w", entry.Name(), err)
		}
		sum := sha256.Sum256(data)
		items = append(items, migration{
			version:  entry.Name(),
			checksum: hex.EncodeToString(sum[:]),
			content:  string(data),
			nonTx:    strings.Contains(entry.Name(), "_notx.sql"),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].version < items[j].version })
	return items, nil
}

func migrationApplied(ctx context.Context, conn *sql.Conn, item migration) (bool, error) {
	var checksum string
	err := conn.QueryRowContext(ctx, "SELECT checksum FROM schema_migrations WHERE version = $1", item.version).Scan(&checksum)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("读取迁移状态 %s: %w", item.version, err)
	}
	if checksum != item.checksum {
		return false, fmt.Errorf("已应用迁移 %s 被修改", item.version)
	}
	return true, nil
}

func applyTransactional(ctx context.Context, conn *sql.Conn, item migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, item.content); err != nil {
		return fmt.Errorf("执行迁移 %s: %w", item.version, err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, checksum) VALUES ($1, $2)", item.version, item.checksum); err != nil {
		return fmt.Errorf("记录迁移 %s: %w", item.version, err)
	}
	return tx.Commit()
}

func applyNonTransactional(ctx context.Context, conn *sql.Conn, item migration) error {
	if _, err := conn.ExecContext(ctx, item.content); err != nil {
		return fmt.Errorf("执行非事务迁移 %s: %w", item.version, err)
	}
	_, err := conn.ExecContext(ctx, "INSERT INTO schema_migrations(version, checksum) VALUES ($1, $2)", item.version, item.checksum)
	return err
}
