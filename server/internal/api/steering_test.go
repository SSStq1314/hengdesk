package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/api"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

// D-33 第 5 条：部门管理员创建的知识库 / 智能体默认只共享给本部门及下级；跨子树只有组织管理员可以。
func TestDeptAdminShareScope(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	az := &authz.Service{DB: f.c}
	da := principal(f.env, "deptadmin@x.local")
	for _, rt := range []string{rbac.ResKB, rbac.ResAgent} {
		if err := az.InitShare(ctx, da, rt, "r1"); err != nil {
			t.Fatal(err)
		}
		for email, want := range map[string]bool{"deptadmin@x.local": true, "dev@x.local": true, "sales@x.local": false, "orgadmin@x.local": false} {
			ok, err := az.Check(ctx, principal(f.env, email), rt, "r1", rbac.BitUse)
			if err != nil || ok != want {
				t.Errorf("%s %s use=%v want %v (%v)", rt, email, ok, want, err)
			}
		}
		if ok, _ := az.Check(ctx, principal(f.env, "dev@x.local"), rt, "r1", rbac.BitEdit); ok {
			t.Errorf("%s: dept members must not get edit by default", rt)
		}
		// 部门管理员：子树内可以，子树外 / 角色主体不行
		if err := az.Share(ctx, da, rt, "r1", authz.Subject{Type: "dept", ID: f.child.ID}, rbac.BitEdit); err != nil {
			t.Errorf("share to child: %v", err)
		}
		if err := az.Share(ctx, da, rt, "r1", authz.Subject{Type: "user", ID: atoi(f.uid(t, "dev@x.local"))}, rbac.BitEdit); err != nil {
			t.Errorf("share to subtree user: %v", err)
		}
		for _, sub := range []authz.Subject{{Type: "dept", ID: f.other.ID}, {Type: "user", ID: atoi(f.uid(t, "sales@x.local"))}, {Type: "role", ID: 1}, {Type: "user", ID: f.org2user}} {
			if err := az.Share(ctx, da, rt, "r1", sub, rbac.BitView); !errors.Is(err, authz.ErrOutsideSubtree) {
				t.Errorf("%s share to %+v: %v", rt, sub, err)
			}
		}
		// 组织管理员可以跨子树共享
		if err := az.Share(ctx, principal(f.env, "orgadmin@x.local"), rt, "r1", authz.Subject{Type: "dept", ID: f.other.ID}, rbac.BitView); err != nil {
			t.Errorf("orgadmin cross share: %v", err)
		}
		if ok, _ := az.Check(ctx, principal(f.env, "sales@x.local"), rt, "r1", rbac.BitView); !ok {
			t.Errorf("%s: sales should see after orgadmin share", rt)
		}
	}
	// 组织管理员自己创建：默认只有本人
	oa := principal(f.env, "orgadmin@x.local")
	if err := az.InitShare(ctx, oa, rbac.ResKB, "r2"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := az.Check(ctx, principal(f.env, "dev@x.local"), rbac.ResKB, "r2", rbac.BitView); ok {
		t.Error("org admin kb must be private by default")
	}
	// 普通成员没有创建权限
	if err := az.InitShare(ctx, principal(f.env, "dev@x.local"), rbac.ResKB, "r3"); !errors.Is(err, authz.ErrOutsideSubtree) {
		t.Errorf("member init share: %v", err)
	}
}

// D-33 第 4 条：指派 / 撤销审计员记审计，审计员能看到。
func TestAuditorAssignRevokeAudited(t *testing.T) {
	f := newFixture(t)
	admin := f.loginAs(t, "admin@x.local", "password123")
	id := f.uid(t, "dev@x.local")
	if rec := admin.do(http.MethodPost, "/api/v1/admin/users/"+id+"/roles", `{"role":"security_auditor"}`, true); rec.Code != http.StatusNoContent {
		t.Fatalf("assign: %d %s", rec.Code, rec.Body)
	}
	if rec := admin.do(http.MethodDelete, "/api/v1/admin/users/"+id+"/roles/security_auditor", "", true); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	aud := f.loginAs(t, "auditor@x.local", "password123")
	for _, act := range []string{"role.assign", "role.revoke"} {
		rec := aud.do(http.MethodGet, "/api/v1/admin/audit-logs?action="+act, "", false)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "security_auditor") || !strings.Contains(rec.Body.String(), `"resource_id":"`+id+`"`) {
			t.Fatalf("%s not visible to auditor: %d %s", act, rec.Code, rec.Body)
		}
	}
	if n := f.c.AuditLog.Query().Where(auditlog.Action("role.assign"), auditlog.ResourceID(id)).CountX(context.Background()); n != 1 {
		t.Fatalf("assign rows %d", n)
	}
}

func TestSystemStatusBanners(t *testing.T) {
	e := newEnv(t)
	admin := e.loginAs(t, "admin@x.local", "password123")
	var st api.SystemStatus
	rec := admin.do(http.MethodGet, "/api/v1/admin/system/status", "", false)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &st) != nil {
		t.Fatalf("status: %d %s", rec.Code, rec.Body)
	}
	if st.AuditorAssigned || st.DiskFreePercent == nil || st.BackupKeep != 5 || st.DiskLowThreshold != 10 || st.HTTPS {
		t.Fatalf("fresh install status: %+v", st)
	}
	f := newFixture(t) // 有审计员
	rec = f.loginAs(t, "admin@x.local", "password123").do(http.MethodGet, "/api/v1/admin/system/status", "", false)
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if !st.AuditorAssigned {
		t.Fatalf("auditor assigned not detected: %s", rec.Body)
	}
	// 普通成员无权查看：404
	if code := f.loginAs(t, "member@x.local", "password123").do(http.MethodGet, "/api/v1/admin/system/status", "", false).Code; code != 404 {
		t.Fatalf("member: %d", code)
	}
}

func TestPasswordPolicyOnReset(t *testing.T) {
	e := newEnv(t)
	admin := e.loginAs(t, "admin@x.local", "password123")
	base := "/api/v1/admin/users/" + e.uid(t, "member@x.local")
	for _, pw := range []string{"short", "Password1", "QWERTY123", "Member-2026x"} {
		if rec := admin.do(http.MethodPost, base+"/password-reset", `{"new_password":"`+pw+`"}`, true); rec.Code != http.StatusBadRequest {
			t.Errorf("%s accepted: %d", pw, rec.Code)
		}
	}
}
