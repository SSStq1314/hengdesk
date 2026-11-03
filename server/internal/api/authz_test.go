package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/role"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
	"github.com/SSStq1314/hengdesk/server/internal/store"
)

type fixture struct {
	*env
	root, child, other *ent.Department
	org2user           int
}

func (e *env) addUser(t *testing.T, email string, org *ent.Organization, roleKey string, dept *ent.Department) *ent.User {
	ctx := context.Background()
	h, _ := bcrypt.GenerateFromPassword([]byte("password123"), bcrypt.MinCost)
	u := e.c.User.Create().SetEmail(email).SetPasswordHash(string(h)).SetOrg(org).SaveX(ctx)
	mc := e.c.Membership.Create().SetUser(u)
	if dept != nil {
		mc.SetDepartmentID(dept.ID)
	}
	if roleKey != "" {
		mc.SetRoleID(e.c.Role.Query().Where(role.Key(roleKey)).OnlyX(ctx).ID)
	} else {
		mc.SetRoleID(e.c.Role.Query().Where(role.Key(rbac.Member)).OnlyX(ctx).ID)
	}
	mc.ExecX(ctx)
	return u
}

func newFixture(t *testing.T) *fixture {
	e := newEnv(t)
	ctx := context.Background()
	org := e.c.Organization.Query().OnlyX(ctx)
	tx, _ := e.c.Tx(ctx)
	root, _ := store.CreateDept(ctx, tx, org.ID, "研发部", nil)
	child, _ := store.CreateDept(ctx, tx, org.ID, "后端组", &root.ID)
	other, _ := store.CreateDept(ctx, tx, org.ID, "市场部", nil)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	e.addUser(t, "auditor@x.local", org, rbac.SecurityAuditor, nil)
	e.addUser(t, "orgadmin@x.local", org, rbac.OrgAdmin, nil)
	e.addUser(t, "deptadmin@x.local", org, rbac.DeptAdmin, root)
	e.addUser(t, "dev@x.local", org, "", child)
	e.addUser(t, "sales@x.local", org, "", other)
	org2 := e.c.Organization.Create().SetName("o2").SetSlug("o2").SaveX(ctx)
	u2 := e.addUser(t, "x@o2.local", org2, rbac.OrgAdmin, nil)
	return &fixture{env: e, root: root, child: child, other: other, org2user: u2.ID}
}

// 5 角色 × 关键接口：未授权一律 404，绝不 403。
func TestRoleMatrix404(t *testing.T) {
	f := newFixture(t)
	type row struct {
		email                 string
		auditLogs, roles, dis int
	}
	cases := []row{
		{"admin@x.local", 404, 200, 204},
		{"auditor@x.local", 200, 200, 404},
		{"orgadmin@x.local", 404, 200, 204},
		{"deptadmin@x.local", 404, 200, 404},
		{"member@x.local", 404, 404, 404},
	}
	for _, c := range cases {
		t.Run(c.email, func(t *testing.T) {
			cl := f.loginAs(t, c.email, "password123")
			if got := cl.do(http.MethodGet, "/api/v1/admin/audit-logs", "", false).Code; got != c.auditLogs {
				t.Errorf("audit-logs %d want %d", got, c.auditLogs)
			}
			if got := cl.do(http.MethodGet, "/api/v1/admin/roles", "", false).Code; got != c.roles {
				t.Errorf("roles %d want %d", got, c.roles)
			}
			target := f.uid(t, "sales@x.local")
			if got := cl.do(http.MethodPost, "/api/v1/admin/users/"+target+"/disable", "", true).Code; got != c.dis {
				t.Errorf("disable %d want %d", got, c.dis)
			}
			f.c.User.UpdateOneID(atoi(target)).SetStatus("active").ExecX(context.Background())
		})
	}
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func TestIDORCrossOrgIs404(t *testing.T) {
	f := newFixture(t)
	cl := f.loginAs(t, "orgadmin@x.local", "password123")
	id := strconv.Itoa(f.org2user)
	for _, p := range []string{"/disable", "/sessions/revoke", "/password-reset"} {
		if got := cl.do(http.MethodPost, "/api/v1/admin/users/"+id+p, `{}`, true).Code; got != 404 {
			t.Errorf("%s cross-org %d", p, got)
		}
	}
	if got := cl.do(http.MethodPost, "/api/v1/admin/users/999999/disable", "", true).Code; got != 404 {
		t.Errorf("nonexistent %d", got)
	}
}

func TestPrivilegedTargetsAndSoD(t *testing.T) {
	f := newFixture(t)
	oa := f.loginAs(t, "orgadmin@x.local", "password123")
	// 组织管理员不能操作超级管理员、也不能授予特权角色：404。
	if got := oa.do(http.MethodPost, "/api/v1/admin/users/"+f.uid(t, "admin@x.local")+"/disable", "", true).Code; got != 404 {
		t.Errorf("org_admin disable super_admin %d", got)
	}
	if got := oa.do(http.MethodPost, "/api/v1/admin/users/"+f.uid(t, "dev@x.local")+"/roles", `{"role":"super_admin"}`, true).Code; got != 404 {
		t.Errorf("org_admin grant super_admin %d", got)
	}
	sa := f.loginAs(t, "admin@x.local", "password123")
	// 三员分立：审计员不能再成为超级管理员。
	if got := sa.do(http.MethodPost, "/api/v1/admin/users/"+f.uid(t, "auditor@x.local")+"/roles", `{"role":"super_admin"}`, true).Code; got != 400 {
		t.Errorf("SoD %d", got)
	}
	// 至少保留 1 名超级管理员：撤销自己、停用均被拒绝。
	if got := sa.do(http.MethodDelete, "/api/v1/admin/users/"+f.uid(t, "admin@x.local")+"/roles/super_admin", "", true).Code; got != 400 {
		t.Errorf("last SA revoke %d", got)
	}
	if got := sa.do(http.MethodPost, "/api/v1/admin/users/"+f.uid(t, "dev@x.local")+"/roles", `{"role":"super_admin"}`, true).Code; got != 204 {
		t.Fatalf("grant SA %d", got)
	}
	dev := f.loginAs(t, "dev@x.local", "password123")
	if got := dev.do(http.MethodPost, "/api/v1/admin/users/"+f.uid(t, "admin@x.local")+"/disable", "", true).Code; got != 204 {
		t.Errorf("disable one of two SA %d", got)
	}
	if got := dev.do(http.MethodDelete, "/api/v1/admin/users/"+f.uid(t, "dev@x.local")+"/roles/super_admin", "", true).Code; got != 400 {
		t.Errorf("last active SA revoke %d", got)
	}
	// 部门管理员必须指定部门。
	if got := dev.do(http.MethodPost, "/api/v1/admin/users/"+f.uid(t, "sales@x.local")+"/roles", `{"role":"dept_admin"}`, true).Code; got != 400 {
		t.Errorf("dept_admin w/o dept %d", got)
	}
}

func principal(e *env, email string) *auth.Principal {
	ctx := context.Background()
	u := e.c.User.Query().Where(user.Email(email)).OnlyX(ctx)
	p, err := auth.LoadPrincipal(ctx, e.c, u.ID)
	if err != nil {
		panic(err)
	}
	return p
}

func TestACLIncludeChildrenAndToolDefaultDisabled(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	az := &authz.Service{DB: f.c}
	org := f.c.Organization.Query().FirstX(ctx)
	dev, sales := principal(f.env, "dev@x.local"), principal(f.env, "sales@x.local")

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// 授权给「研发部」不含下级：后端组成员不可见。
	must(az.Grant(ctx, org.ID, 1, rbac.ResKB, "kb1", authz.Subject{Type: "dept", ID: f.root.ID}, rbac.BitView))
	if ok, _ := az.Check(ctx, dev, rbac.ResKB, "kb1", rbac.BitView); ok {
		t.Fatal("no include_children should not reach child dept")
	}
	must(az.Grant(ctx, org.ID, 1, rbac.ResKB, "kb1", authz.Subject{Type: "dept", ID: f.root.ID, IncludeChildren: true}, rbac.BitEdit))
	if ok, _ := az.Check(ctx, dev, rbac.ResKB, "kb1", rbac.BitView); !ok {
		t.Fatal("include_children + edit should imply view for child dept")
	}
	if ok, _ := az.Check(ctx, dev, rbac.ResKB, "kb1", rbac.BitManage); ok {
		t.Fatal("edit must not imply manage")
	}
	if ok, _ := az.Check(ctx, sales, rbac.ResKB, "kb1", rbac.BitView); ok {
		t.Fatal("other dept must not see")
	}
	ids, err := az.VisibleIDs(ctx, dev, rbac.ResKB, rbac.BitView)
	if err != nil || len(ids) != 1 || ids[0] != "kb1" {
		t.Fatalf("visible %v %v", ids, err)
	}

	// 新工具默认禁用：即使有 use 授权也不可用。
	tool := f.c.Tool.Create().SetKey("t1").SetName("t1").SaveX(ctx)
	must(az.Grant(ctx, org.ID, 1, rbac.ResTool, tool.Key, authz.Subject{Type: "user", ID: dev.ID}, rbac.BitUse))
	if ok, _ := az.ToolUsable(ctx, dev, tool); ok {
		t.Fatal("new tool must be disabled by default")
	}
	tool = tool.Update().SetEnabled(true).SaveX(ctx)
	if ok, _ := az.ToolUsable(ctx, dev, tool); !ok {
		t.Fatal("enabled + use grant should be usable")
	}
	if ok, _ := az.ToolUsable(ctx, sales, tool); ok {
		t.Fatal("no grant must not be usable")
	}
}

func jsonStr(m map[string]any) string {
	b, _ := json.Marshal(m)
	return string(b)
}
