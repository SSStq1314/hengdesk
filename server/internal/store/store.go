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
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
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

// CreateSchemaForTest 仅供单元测试（SQLite）：用 ent 自动建表。生产环境一律走版本化迁移（ApplyMigrations）。
func CreateSchemaForTest(ctx context.Context, c *ent.Client) error {
	if err := c.Schema.Create(ctx); err != nil {
		return fmt.Errorf("建表: %w", err)
	}
	return nil
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
	if err := SyncBuiltinRoles(ctx, c); err != nil {
		return false, err
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
	sa, err := c.Role.Query().Where(role.Key(rbac.SuperAdmin)).Only(ctx)
	if err != nil {
		return false, err
	}
	u, err := c.User.Create().SetEmail(email).SetDisplayName("超级管理员").
		SetPasswordHash(string(hash)).SetOrg(org).Save(ctx)
	if err != nil {
		return false, err
	}
	if err := c.Membership.Create().SetUser(u).SetRole(sa).SetIsPrimary(true).Exec(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// SyncBuiltinRoles 幂等地写入 5 个内置角色，并把名称和权限点同步为代码中的定义。
func SyncBuiltinRoles(ctx context.Context, c *ent.Client) error {
	for _, r := range rbac.Builtin {
		perms := make([]string, 0, len(r.Perms))
		for _, p := range r.Perms {
			perms = append(perms, string(p))
		}
		if err := c.Role.Create().SetKey(r.Key).SetName(r.Name).SetBuiltin(true).SetPermissions(perms).
			OnConflictColumns(role.FieldKey).
			Update(func(u *ent.RoleUpsert) { u.UpdateName().UpdatePermissions().UpdateBuiltin() }).
			Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}
