// Package store 负责数据库连接、迁移与初始数据。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib" // postgres 驱动

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/organization"
	"github.com/SSStq1314/hengdesk/server/internal/ent/role"
	_ "github.com/SSStq1314/hengdesk/server/internal/ent/runtime" // 注册 schema hooks（审计 append-only）
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"golang.org/x/crypto/bcrypt"
)

// Open 打开 PostgreSQL。
func Open(dsn string) (*ent.Client, *sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, nil, err
	}
	return ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db))), db, nil
}

// appendOnlySQL 在数据库层强制审计表只追加。
const appendOnlySQL = `
CREATE OR REPLACE FUNCTION hengdesk_audit_immutable() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'audit_logs is append-only (% rejected)', TG_OP;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS audit_logs_no_update ON audit_logs;
CREATE TRIGGER audit_logs_no_update BEFORE UPDATE OR DELETE ON audit_logs
  FOR EACH ROW EXECUTE FUNCTION hengdesk_audit_immutable();
DROP TRIGGER IF EXISTS audit_logs_no_truncate ON audit_logs;
CREATE TRIGGER audit_logs_no_truncate BEFORE TRUNCATE ON audit_logs
  FOR EACH STATEMENT EXECUTE FUNCTION hengdesk_audit_immutable();
`

// Migrate 建表；在 PostgreSQL 上额外启用 pgvector 并安装审计只追加触发器。
func Migrate(ctx context.Context, c *ent.Client, db *sql.DB, postgres bool) error {
	if postgres {
		if _, err := db.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
			return fmt.Errorf("启用 pgvector: %w", err)
		}
	}
	if err := c.Schema.Create(ctx); err != nil {
		return fmt.Errorf("建表: %w", err)
	}
	if postgres {
		if _, err := db.ExecContext(ctx, appendOnlySQL); err != nil {
			return fmt.Errorf("审计触发器: %w", err)
		}
	}
	return nil
}

// BuiltinRoles 内置角色。
var BuiltinRoles = []struct{ Key, Name string }{
	{"super_admin", "超级管理员"},
	{"admin", "管理员"},
	{"auditor", "审计员"},
	{"approver", "审批人"},
	{"member", "普通成员"},
}

// SeedOptions 初始数据参数。
type SeedOptions struct {
	OrgName, OrgSlug string
	AdminEmail       string
	AdminPassword    string
}

// Seed 幂等地创建默认组织、内置角色与初始超级管理员。
// 管理员已存在时不会覆盖其密码。
func Seed(ctx context.Context, c *ent.Client, o SeedOptions) (created bool, err error) {
	org, err := c.Organization.Query().Where(organization.Slug(o.OrgSlug)).Only(ctx)
	if ent.IsNotFound(err) {
		org, err = c.Organization.Create().SetName(o.OrgName).SetSlug(o.OrgSlug).Save(ctx)
	}
	if err != nil {
		return false, err
	}
	for _, r := range BuiltinRoles {
		if err := c.Role.Create().SetKey(r.Key).SetName(r.Name).SetBuiltin(true).
			OnConflictColumns(role.FieldKey).Ignore().Exec(ctx); err != nil {
			return false, err
		}
	}
	email := strings.ToLower(strings.TrimSpace(o.AdminEmail))
	exists, err := c.User.Query().Where(user.Email(email)).Exist(ctx)
	if err != nil || exists {
		return false, err
	}
	if len(o.AdminPassword) < 8 {
		return false, errors.New("首次初始化需设置 HENGDESK_ADMIN_PASSWORD（至少 8 位）")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(o.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		return false, err
	}
	sa, err := c.Role.Query().Where(role.Key("super_admin")).Only(ctx)
	if err != nil {
		return false, err
	}
	u, err := c.User.Create().SetEmail(email).SetDisplayName("超级管理员").
		SetPasswordHash(string(hash)).SetIsSuperAdmin(true).SetOrg(org).Save(ctx)
	if err != nil {
		return false, err
	}
	if err := c.Membership.Create().SetUser(u).SetRole(sa).SetIsPrimary(true).Exec(ctx); err != nil {
		return false, err
	}
	return true, nil
}
