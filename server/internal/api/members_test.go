package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/department"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
)

type listResp struct {
	Items []struct {
		ID          int    `json:"id"`
		Email       string `json:"email"`
		EmployeeNo  string `json:"employee_no"`
		DisplayName string `json:"display_name"`
		Departments []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"departments"`
	} `json:"items"`
	NextBeforeID int `json:"next_before_id"`
}

func emailsOf(t *testing.T, body []byte) []string {
	var r listResp
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, it := range r.Items {
		out = append(out, it.Email)
	}
	return out
}

func TestMembersListScopeAndCRUD(t *testing.T) {
	f := newFixture(t)
	oa := f.loginAs(t, "orgadmin@x.local", "password123")
	rec := oa.do(http.MethodGet, "/api/v1/admin/users?limit=200", "", false)
	if rec.Code != 200 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	all := strings.Join(emailsOf(t, rec.Body.Bytes()), ",")
	if !strings.Contains(all, "sales@x.local") || strings.Contains(all, "x@o2.local") {
		t.Fatalf("org admin list: %s", all)
	}
	// 部门管理员只看到子树
	da := f.loginAs(t, "deptadmin@x.local", "password123")
	rec = da.do(http.MethodGet, "/api/v1/admin/users", "", false)
	got := strings.Join(emailsOf(t, rec.Body.Bytes()), ",")
	if !strings.Contains(got, "dev@x.local") || strings.Contains(got, "sales@x.local") {
		t.Fatalf("dept admin list: %s", got)
	}
	if c := da.do(http.MethodGet, "/api/v1/admin/users/"+f.uid(t, "sales@x.local"), "", false).Code; c != 404 {
		t.Fatalf("dept admin get outside subtree: %d", c)
	}
	// 按部门过滤（含下级）
	rec = oa.do(http.MethodGet, "/api/v1/admin/users?dept_id="+strconv.Itoa(f.root.ID), "", false)
	got = strings.Join(emailsOf(t, rec.Body.Bytes()), ",")
	if !strings.Contains(got, "dev@x.local") || strings.Contains(got, "sales@x.local") {
		t.Fatalf("dept filter: %s", got)
	}
	// 新建
	body := `{"email":" New@X.local ","display_name":"新人","employee_no":"E001","department_ids":[` + strconv.Itoa(f.child.ID) + `]}`
	rec = oa.do(http.MethodPost, "/api/v1/admin/users", body, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if c := oa.do(http.MethodPost, "/api/v1/admin/users", body, true).Code; c != http.StatusConflict {
		t.Fatalf("dup email: %d", c)
	}
	if c := oa.do(http.MethodPost, "/api/v1/admin/users", `{"email":"n2@x.local","employee_no":"E001"}`, true).Code; c != http.StatusConflict {
		t.Fatalf("dup employee_no: %d", c)
	}
	if c := oa.do(http.MethodPost, "/api/v1/admin/users", `{"email":"n3@x.local","password":"password"}`, true).Code; c != http.StatusBadRequest {
		t.Fatalf("weak password: %d", c)
	}
	if c := oa.do(http.MethodPost, "/api/v1/admin/users", `{"email":"bad"}`, true).Code; c != http.StatusBadRequest {
		t.Fatalf("bad email: %d", c)
	}
	// 部门管理员不能把人建到子树外
	if c := da.do(http.MethodPost, "/api/v1/admin/users", `{"email":"n4@x.local","department_ids":[`+strconv.Itoa(f.other.ID)+`]}`, true).Code; c != 404 {
		t.Fatalf("dept admin create outside: %d", c)
	}
	// 他组织部门 → 404
	if c := oa.do(http.MethodPost, "/api/v1/admin/users", `{"email":"n5@x.local","department_ids":[999999]}`, true).Code; c != 404 {
		t.Fatalf("foreign dept: %d", c)
	}
	// 修改部门
	id := f.uid(t, "new@x.local")
	rec = oa.do(http.MethodPatch, "/api/v1/admin/users/"+id, `{"display_name":"改名","department_ids":[`+strconv.Itoa(f.other.ID)+`]}`, true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "市场部") || strings.Contains(rec.Body.String(), "后端组") {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	// 普通成员 / 他组织 → 404
	dev := f.loginAs(t, "dev@x.local", "password123")
	if c := dev.do(http.MethodGet, "/api/v1/admin/users", "", false).Code; c != 404 {
		t.Fatalf("member list: %d", c)
	}
	if c := oa.do(http.MethodPatch, "/api/v1/admin/users/"+strconv.Itoa(f.org2user), `{"display_name":"x"}`, true).Code; c != 404 {
		t.Fatalf("cross org patch: %d", c)
	}
}

func TestDepartmentsCRUD(t *testing.T) {
	f := newFixture(t)
	oa := f.loginAs(t, "orgadmin@x.local", "password123")
	rec := oa.do(http.MethodPost, "/api/v1/admin/departments", `{"name":"测试组","parent_id":`+strconv.Itoa(f.root.ID)+`}`, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var d struct{ ID int }
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	if c := oa.do(http.MethodPost, "/api/v1/admin/departments", `{"name":"测试组","parent_id":`+strconv.Itoa(f.root.ID)+`}`, true).Code; c != http.StatusConflict {
		t.Fatalf("dup: %d", c)
	}
	if c := oa.do(http.MethodPost, "/api/v1/admin/departments", `{"name":"a/b"}`, true).Code; c != http.StatusBadRequest {
		t.Fatalf("slash: %d", c)
	}
	if c := oa.do(http.MethodPatch, "/api/v1/admin/departments/"+strconv.Itoa(d.ID), `{"name":"测试二组"}`, true).Code; c != 200 {
		t.Fatalf("rename: %d", c)
	}
	// 非空部门不能删
	if c := oa.do(http.MethodDelete, "/api/v1/admin/departments/"+strconv.Itoa(f.child.ID), "", true).Code; c != http.StatusBadRequest {
		t.Fatalf("delete non-empty: %d", c)
	}
	if c := oa.do(http.MethodDelete, "/api/v1/admin/departments/"+strconv.Itoa(d.ID), "", true).Code; c != http.StatusNoContent {
		t.Fatalf("delete: %d", c)
	}
	// 部门管理员（M1 只读部门树，见 D-30）：看不到市场部；任何写操作 404
	da := f.loginAs(t, "deptadmin@x.local", "password123")
	rec = da.do(http.MethodGet, "/api/v1/admin/departments", "", false)
	if strings.Contains(rec.Body.String(), "市场部") || !strings.Contains(rec.Body.String(), "后端组") {
		t.Fatalf("dept admin tree: %s", rec.Body)
	}
	if c := da.do(http.MethodPost, "/api/v1/admin/departments", `{"name":"顶层"}`, true).Code; c != 404 {
		t.Fatalf("dept admin top-level: %d", c)
	}
	if c := da.do(http.MethodPost, "/api/v1/admin/departments", `{"name":"x","parent_id":`+strconv.Itoa(f.other.ID)+`}`, true).Code; c != 404 {
		t.Fatalf("dept admin outside: %d", c)
	}
	if c := da.do(http.MethodPatch, "/api/v1/admin/departments/"+strconv.Itoa(f.other.ID), `{"name":"x"}`, true).Code; c != 404 {
		t.Fatalf("dept admin rename outside: %d", c)
	}
	if c := da.do(http.MethodPost, "/api/v1/admin/departments", `{"name":"子组","parent_id":`+strconv.Itoa(f.child.ID)+`}`, true).Code; c != 404 {
		t.Fatalf("dept admin has no DeptManage in M1: %d", c)
	}
	// 部门管理员不能导入（组织级操作）
	if c := da.do(http.MethodPost, "/api/v1/admin/departments/import", "dept_path,name,email\n研发部,a,a@x.local\n", true).Code; c != 404 {
		t.Fatalf("dept admin import: %d", c)
	}
}

func TestDepartmentCSVImport(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	oa := f.loginAs(t, "orgadmin@x.local", "password123")
	csv := "\ufeffdept_path,name,email,employee_no\n" +
		"研发部/后端组,开发,dev@x.local,E100\n" +
		"研发部/前端组,小前,fe@x.local,E101\n" +
		"销售中心/华东/上海,小沪,sh@x.local,\n"
	rec := oa.do(http.MethodPost, "/api/v1/admin/departments/import?dry_run=1", csv, true)
	if rec.Code != 200 {
		t.Fatalf("dry run: %d %s", rec.Code, rec.Body)
	}
	var r struct {
		Applied      bool     `json:"applied"`
		NewDepts     []string `json:"new_departments"`
		CreatedUsers int      `json:"created_users"`
		UpdatedUsers int      `json:"updated_users"`
		Errors       []struct {
			Line    int
			Message string
		} `json:"errors"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &r)
	if r.Applied || r.CreatedUsers != 2 || r.UpdatedUsers != 1 || strings.Join(r.NewDepts, ",") != "研发部/前端组,销售中心,销售中心/华东,销售中心/华东/上海" {
		t.Fatalf("plan: %+v", r)
	}
	if f.c.User.Query().Where(user.Email("fe@x.local")).ExistX(ctx) {
		t.Fatal("dry run wrote data")
	}
	rec = oa.do(http.MethodPost, "/api/v1/admin/departments/import", csv, true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"applied":true`) {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	sh := f.c.Department.Query().Where(department.Name("上海")).OnlyX(ctx)
	hd := f.c.Department.Query().Where(department.Name("华东")).OnlyX(ctx)
	if sh.ParentID == nil || *sh.ParentID != hd.ID {
		t.Fatal("hierarchy not built")
	}
	dev := f.c.User.Query().Where(user.Email("dev@x.local")).OnlyX(ctx)
	if dev.EmployeeNo != "E100" || dev.DisplayName != "开发" {
		t.Fatalf("update: %+v", dev)
	}
	fe := f.c.User.Query().Where(user.Email("fe@x.local")).OnlyX(ctx)
	if fe.PasswordHash != "" {
		t.Fatal("imported users must not get a password")
	}
	// 新部门在子树过滤中可见（闭包表已维护）
	rec = oa.do(http.MethodGet, "/api/v1/admin/users?dept_id="+strconv.Itoa(f.root.ID), "", false)
	if !strings.Contains(rec.Body.String(), "fe@x.local") {
		t.Fatalf("closure: %s", rec.Body)
	}
	if !f.c.AuditLog.Query().Where(auditlog.Action("dept.import")).ExistX(ctx) {
		t.Fatal("import not audited")
	}
	// 幂等：再导一次不新建
	rec = oa.do(http.MethodPost, "/api/v1/admin/departments/import?dry_run=1", csv, true)
	_ = json.Unmarshal(rec.Body.Bytes(), &r)
	if len(r.NewDepts) != 0 || r.CreatedUsers != 0 {
		t.Fatalf("idempotent: %+v", r)
	}
	// 错误：整批不写入，返回行号
	bad := "dept_path,name,email,employee_no\n研发部,a,ok@x.local,\n研发部,b,not-an-email,\n研发部,c,ok@x.local,\n研发部,d,x@o2.local,\n研发部,e,e@x.local,E100\n,f,f@x.local,\n"
	rec = oa.do(http.MethodPost, "/api/v1/admin/departments/import", bad, true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad: %d %s", rec.Code, rec.Body)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &r)
	lines := map[int]bool{}
	for _, e := range r.Errors {
		lines[e.Line] = true
	}
	for _, l := range []int{3, 4, 5, 6, 7} {
		if !lines[l] {
			t.Errorf("missing error for line %d: %+v", l, r.Errors)
		}
	}
	if f.c.User.Query().Where(user.Email("ok@x.local")).ExistX(ctx) {
		t.Fatal("partial write on error")
	}
	if c := oa.do(http.MethodPost, "/api/v1/admin/departments/import", "name,email\na,a@x.local\n", true).Code; c != http.StatusUnprocessableEntity {
		t.Fatalf("missing column: %d", c)
	}
}
