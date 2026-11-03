package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"

	atlasmigrate "ariga.io/atlas/sql/migrate"
)

// 迁移策略（D-30 / D-31）：只向前执行，无 down 迁移；回滚 = 从升级前的 pg_dump 备份恢复。
//
//go:embed migrations/*.sql migrations/atlas.sum
var migrationsFS embed.FS

const revisionsDDL = `CREATE TABLE IF NOT EXISTS hengdesk_schema_revisions (
  version text PRIMARY KEY,
  name text NOT NULL,
  hash text NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT now()
)`

// migrationLockID 是迁移的 PostgreSQL advisory lock 键，防止多个 migrate 并发执行。
const migrationLockID = 7231_2026

// ErrLegacySchema M0 预览版用 ent 自动建表创建的库，没有迁移记录，不支持原地升级。
var ErrLegacySchema = errors.New("检测到 M0 预览版数据库（无迁移记录）：不支持原地升级，请备份后重新安装")

// MigrationDir 返回内嵌的迁移目录，并校验 atlas.sum（防止迁移文件被篡改或漏提交）。
func MigrationDir() (atlasmigrate.Dir, error) {
	mem := &atlasmigrate.MemDir{}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		b, err := migrationsFS.ReadFile(path.Join("migrations", e.Name()))
		if err != nil {
			return nil, err
		}
		if err := mem.WriteFile(e.Name(), b); err != nil {
			return nil, err
		}
	}
	if err := atlasmigrate.Validate(mem); err != nil {
		return nil, fmt.Errorf("迁移目录校验失败: %w", err)
	}
	return mem, nil
}

// MigrationStatus 迁移状态。
type MigrationStatus struct {
	Applied []string
	Pending []string
	Fresh   bool // 空库（无任何业务表）
}

func fileHash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// Status 读取已执行与待执行的迁移；已执行文件内容被改动时报错。
func Status(ctx context.Context, db *sql.DB) (*MigrationStatus, error) {
	dir, err := MigrationDir()
	if err != nil {
		return nil, err
	}
	files, err := dir.Files()
	if err != nil {
		return nil, err
	}
	st := &MigrationStatus{}
	var hasRev, hasUsers bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('hengdesk_schema_revisions') IS NOT NULL, to_regclass('users') IS NOT NULL`).Scan(&hasRev, &hasUsers); err != nil {
		return nil, err
	}
	applied := map[string]string{}
	if hasRev {
		rows, err := db.QueryContext(ctx, `SELECT version, hash FROM hengdesk_schema_revisions`)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var v, h string
			if err := rows.Scan(&v, &h); err != nil {
				return nil, err
			}
			applied[v] = h
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	} else if hasUsers {
		return nil, ErrLegacySchema
	}
	st.Fresh = len(applied) == 0
	known := map[string]bool{}
	for _, f := range files {
		known[f.Version()] = true
		if h, ok := applied[f.Version()]; ok {
			if h != fileHash(f.Bytes()) {
				return nil, fmt.Errorf("已执行的迁移 %s 内容被修改（迁移只能追加新文件）", f.Name())
			}
			st.Applied = append(st.Applied, f.Name())
		} else {
			st.Pending = append(st.Pending, f.Name())
		}
	}
	for v := range applied {
		if !known[v] {
			return nil, fmt.Errorf("数据库版本 %s 比当前程序新：禁止降级运行，请从备份恢复或使用新版本", v)
		}
	}
	return st, nil
}

// ApplyMigrations 在 advisory lock 下按顺序执行待执行迁移，每个文件一个事务。
func ApplyMigrations(ctx context.Context, db *sql.DB) (applied []string, err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return nil, err
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockID)
	}()
	st, err := Status(ctx, db)
	if err != nil {
		return nil, err
	}
	if len(st.Pending) == 0 {
		return nil, nil
	}
	if _, err := conn.ExecContext(ctx, revisionsDDL); err != nil {
		return nil, err
	}
	dir, err := MigrationDir()
	if err != nil {
		return nil, err
	}
	files, err := dir.Files()
	if err != nil {
		return nil, err
	}
	pending := map[string]bool{}
	for _, n := range st.Pending {
		pending[n] = true
	}
	for _, f := range files {
		if !pending[f.Name()] {
			continue
		}
		stmts, err := f.Stmts()
		if err != nil {
			return applied, fmt.Errorf("%s: %w", f.Name(), err)
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return applied, err
		}
		for _, s := range stmts {
			if _, err := tx.ExecContext(ctx, s); err != nil {
				_ = tx.Rollback()
				return applied, fmt.Errorf("执行 %s 失败: %w", f.Name(), err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO hengdesk_schema_revisions(version, name, hash) VALUES ($1, $2, $3)`,
			f.Version(), f.Name(), fileHash(f.Bytes())); err != nil {
			_ = tx.Rollback()
			return applied, err
		}
		if err := tx.Commit(); err != nil {
			return applied, err
		}
		applied = append(applied, f.Name())
	}
	return applied, nil
}
