package rbac

import "testing"

func TestSeparationOfDuties(t *testing.T) {
	sa := Resolve([]Grant{{Role: SuperAdmin}})
	if sa.Has(AuditRead) || sa.Has(AuditExport) {
		t.Fatal("超级管理员不应能查看审计")
	}
	au := Resolve([]Grant{{Role: SecurityAuditor}})
	for _, p := range []Perm{MemberManage, DeptManage, RoleAssign, RoleAssignPriv, SSOManage, SettingsManage, ToolManage, ModelManage, BudgetManage, EgressManage} {
		if au.Has(p) {
			t.Fatalf("审计员不应有 %s", p)
		}
	}
	if !au.Has(EgressLogRead) || !sa.Has(EgressLogRead) || !sa.Has(EgressManage) {
		t.Fatal("超级管理员管白名单，审计员只读出站日志")
	}
	if !au.Has(AuditRead) {
		t.Fatal("审计员应能查看审计")
	}
	if _, bad := Conflicts([]string{SuperAdmin}, SecurityAuditor); !bad {
		t.Fatal("互斥未生效")
	}
	if _, bad := Conflicts([]string{SecurityAuditor}, SuperAdmin); !bad {
		t.Fatal("互斥未生效（反向）")
	}
	if _, bad := Conflicts([]string{OrgAdmin}, SecurityAuditor); bad {
		t.Fatal("误报互斥")
	}
}

func TestDeptScoped(t *testing.T) {
	s := Resolve([]Grant{{Role: DeptAdmin, Dept: 7}, {Role: DeptAdmin}, {Role: "unknown"}})
	if s.Has(MemberRead) {
		t.Fatal("部门管理员不应有组织范围权限")
	}
	roots, org := s.DeptRoots(MemberRead)
	if org || len(roots) != 1 || roots[0] != 7 {
		t.Fatalf("roots %v %v", roots, org)
	}
	if !s.HasAny(MemberRead) {
		t.Fatal("HasAny")
	}
	if len(Resolve([]Grant{{Role: Member}}).List()) != 0 {
		t.Fatal("成员不应有管理权限")
	}
}

func TestBits(t *testing.T) {
	if Normalize(BitManage) != 15 || Normalize(BitEdit) != 7 || Normalize(BitUse) != 3 || Normalize(BitView) != 1 || Normalize(64) != 0 {
		t.Fatal("normalize")
	}
	if b, ok := ParseBits([]string{"use"}); !ok || b != 3 {
		t.Fatal("parse")
	}
	if _, ok := ParseBits([]string{"root"}); ok {
		t.Fatal("unknown bit accepted")
	}
}
