package api

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	entacl "github.com/SSStq1314/hengdesk/server/internal/ent/acl"
	"github.com/SSStq1314/hengdesk/server/internal/ent/department"
	"github.com/SSStq1314/hengdesk/server/internal/ent/deptclosure"
	"github.com/SSStq1314/hengdesk/server/internal/ent/membership"
	"github.com/SSStq1314/hengdesk/server/internal/ent/role"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
	"github.com/SSStq1314/hengdesk/server/internal/store"
)

const (
	maxDeptName   = 64
	maxDeptDepth  = 10
	MaxImportRows = 5000
	maxImportSize = 4 << 20
)

type deptHandlers struct {
	db *ent.Client
	az *authz.Service
}

type deptView struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	ParentID    *int   `json:"parent_id"`
	MemberCount int    `json:"member_count"`
}

// scopeIDs 返回操作者在 perm 下可见的部门 ID；orgWide=true 时为 nil。
func (h *deptHandlers) scopeIDs(c echo.Context, perm rbac.Perm) ([]int, bool, error) {
	p := auth.Current(c)
	roots, orgWide := p.Perms.DeptRoots(perm)
	if orgWide {
		return nil, true, nil
	}
	if len(roots) == 0 {
		return nil, false, authz.Deny(c, "scope:"+string(perm))
	}
	ids, err := h.az.Subtree(c.Request().Context(), p.OrgID, roots)
	return ids, false, err
}

// list GET /admin/departments：扁平列表，前端组树；部门管理员只看到自己的子树。
func (h *deptHandlers) list(c echo.Context) error {
	p := auth.Current(c)
	ctx := c.Request().Context()
	ids, orgWide, err := h.scopeIDs(c, rbac.DeptRead)
	if err != nil {
		return err
	}
	q := h.db.Department.Query().Where(department.OrgID(p.OrgID))
	if !orgWide {
		q.Where(department.IDIn(ids...))
	}
	ds, err := q.Order(ent.Asc(department.FieldName)).All(ctx)
	if err != nil {
		return err
	}
	var counts []struct {
		DepartmentID int `json:"department_id"`
		Count        int `json:"count"`
	}
	if err := h.db.Membership.Query().Where(membership.HasDepartmentWith(department.OrgID(p.OrgID)), membership.HasRoleWith(role.Key(rbac.Member))).
		GroupBy(membership.FieldDepartmentID).Aggregate(ent.Count()).Scan(ctx, &counts); err != nil {
		return err
	}
	cm := map[int]int{}
	for _, x := range counts {
		cm[x.DepartmentID] = x.Count
	}
	visible := map[int]bool{}
	for _, d := range ds {
		visible[d.ID] = true
	}
	out := make([]deptView, 0, len(ds))
	for _, d := range ds {
		parent := d.ParentID
		if parent != nil && !visible[*parent] {
			parent = nil // 子树根对部门管理员显示为顶层
		}
		out = append(out, deptView{ID: d.ID, Name: d.Name, ParentID: parent, MemberCount: cm[d.ID]})
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out})
}

func cleanDeptName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxDeptName || strings.Contains(s, "/") {
		return "", fmt.Errorf("部门名称须为 1～%d 个字符，且不能包含 /", maxDeptName)
	}
	return s, nil
}

func (h *deptHandlers) inScope(c echo.Context, id int) error {
	ids, orgWide, err := h.scopeIDs(c, rbac.DeptManage)
	if err != nil || orgWide {
		return err
	}
	for _, x := range ids {
		if x == id {
			return nil
		}
	}
	return authz.Deny(c, "dept_scope")
}

func (h *deptHandlers) load(c echo.Context) (*ent.Department, error) {
	id, _ := strconv.Atoi(c.Param("id"))
	d, err := h.db.Department.Query().Where(department.ID(id), department.OrgID(auth.Current(c).OrgID)).Only(c.Request().Context())
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, authz.Deny(c, "idor:dept")
		}
		return nil, err
	}
	return d, h.inScope(c, d.ID)
}

var errDeptExists = echo.NewHTTPError(http.StatusConflict, "同级已存在同名部门")

// create POST /admin/departments {name, parent_id}
func (h *deptHandlers) create(c echo.Context) error {
	var req struct {
		Name     string `json:"name"`
		ParentID *int   `json:"parent_id"`
	}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "请求格式错误")
	}
	name, err := cleanDeptName(req.Name)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	p := auth.Current(c)
	ctx := c.Request().Context()
	if req.ParentID == nil {
		// 顶层部门只允许组织范围的部门管理权限。
		if _, orgWide := p.Perms.DeptRoots(rbac.DeptManage); !orgWide {
			return authz.Deny(c, "dept_scope")
		}
	} else {
		c.SetParamNames("id")
		c.SetParamValues(strconv.Itoa(*req.ParentID))
		if _, err := h.load(c); err != nil {
			return err
		}
		if n, err := h.db.DeptClosure.Query().Where(deptclosure.DescendantID(*req.ParentID)).Count(ctx); err != nil {
			return err
		} else if n >= maxDeptDepth {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("部门层级最多 %d 级", maxDeptDepth))
		}
	}
	var d *ent.Department
	err = withTx(ctx, h.db, func(tx *ent.Tx) error {
		var e error
		d, e = store.CreateDept(ctx, tx, p.OrgID, name, req.ParentID)
		return e
	})
	if ent.IsConstraintError(err) {
		return errDeptExists
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, deptView{ID: d.ID, Name: d.Name, ParentID: d.ParentID})
}

// rename PATCH /admin/departments/:id {name}（M1 只支持改名，移动部门待确认）
func (h *deptHandlers) rename(c echo.Context) error {
	d, err := h.load(c)
	if err != nil {
		return err
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "请求格式错误")
	}
	name, err := cleanDeptName(req.Name)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	d, err = d.Update().SetName(name).Save(c.Request().Context())
	if ent.IsConstraintError(err) {
		return errDeptExists
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, deptView{ID: d.ID, Name: d.Name, ParentID: d.ParentID})
}

// remove DELETE /admin/departments/:id：仅允许删除没有子部门、没有成员的部门；同时清理指向它的授权。
func (h *deptHandlers) remove(c echo.Context) error {
	d, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	if n, err := h.db.Department.Query().Where(department.ParentID(d.ID)).Count(ctx); err != nil {
		return err
	} else if n > 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "请先删除或移走子部门")
	}
	if n, err := h.db.Membership.Query().Where(membership.DepartmentID(d.ID)).Count(ctx); err != nil {
		return err
	} else if n > 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "部门下仍有成员或角色授权，请先移除")
	}
	err = withTx(ctx, h.db, func(tx *ent.Tx) error {
		if _, err := tx.ACL.Delete().Where(entacl.SubjectTypeEQ(entacl.SubjectTypeDept), entacl.SubjectID(d.ID), entacl.OrgID(d.OrgID)).Exec(ctx); err != nil {
			return err
		}
		if _, err := tx.DeptClosure.Delete().Where(deptclosure.DescendantID(d.ID)).Exec(ctx); err != nil {
			return err
		}
		return tx.Department.DeleteOneID(d.ID).Exec(ctx)
	})
	if err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// ---- CSV 导入 ----

type importError struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

type importRow struct {
	line       int
	path       []string
	name       string
	email      string
	employeeNo string
	existing   *ent.User
}

// ImportResult 导入预览 / 结果。有任何行错误时整批不写入。
type ImportResult struct {
	DryRun       bool          `json:"dry_run"`
	Applied      bool          `json:"applied"`
	Rows         int           `json:"rows"`
	NewDepts     []string      `json:"new_departments"`
	CreatedUsers int           `json:"created_users"`
	UpdatedUsers int           `json:"updated_users"`
	Errors       []importError `json:"errors"`
}

func readImportBody(c echo.Context) ([]byte, error) {
	r := c.Request()
	var src io.Reader = r.Body
	if strings.HasPrefix(r.Header.Get(echo.HeaderContentType), echo.MIMEMultipartForm) {
		fh, err := c.FormFile("file")
		if err != nil {
			return nil, echo.NewHTTPError(http.StatusBadRequest, "缺少上传文件 file")
		}
		f, err := fh.Open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		src = f
	}
	b, err := io.ReadAll(io.LimitReader(src, maxImportSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxImportSize {
		return nil, echo.NewHTTPError(http.StatusRequestEntityTooLarge, "文件不能超过 4 MB")
	}
	return bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), nil
}

// parseImport 解析 CSV：表头必须包含 dept_path,name,email，可选 employee_no。
func parseImport(b []byte) ([]importRow, []importError) {
	if !utf8.Valid(b) {
		return nil, []importError{{Line: 1, Message: "文件须为 UTF-8 编码"}}
	}
	cr := csv.NewReader(bytes.NewReader(b))
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	head, err := cr.Read()
	if err != nil {
		return nil, []importError{{Line: 1, Message: "无法读取表头"}}
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, k := range []string{"dept_path", "name", "email"} {
		if _, ok := col[k]; !ok {
			return nil, []importError{{Line: 1, Message: "表头缺少列 " + k + "（需要 dept_path,name,email,employee_no）"}}
		}
	}
	get := func(rec []string, k string) string {
		if i, ok := col[k]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	var rows []importRow
	var errs []importError
	emails := map[string]int{}
	empNos := map[string]int{}
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			errs = append(errs, importError{Line: line, Message: "CSV 格式错误"})
			break
		}
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue
		}
		if len(rows) >= MaxImportRows {
			errs = append(errs, importError{Line: line, Message: fmt.Sprintf("单次最多导入 %d 行", MaxImportRows)})
			break
		}
		r := importRow{line: line, name: get(rec, "name"), email: normEmail(get(rec, "email")), employeeNo: get(rec, "employee_no")}
		fail := func(m string) { errs = append(errs, importError{Line: line, Message: m}) }
		if !validEmail(r.email) {
			fail("邮箱格式不正确")
		} else if prev, dup := emails[r.email]; dup {
			fail(fmt.Sprintf("邮箱与第 %d 行重复", prev))
		} else {
			emails[r.email] = line
		}
		if r.employeeNo != "" {
			if prev, dup := empNos[r.employeeNo]; dup {
				fail(fmt.Sprintf("工号与第 %d 行重复", prev))
			}
			empNos[r.employeeNo] = line
		}
		if utf8.RuneCountInString(r.name) > 128 {
			fail("姓名过长")
		}
		raw := get(rec, "dept_path")
		if raw == "" {
			fail("dept_path 不能为空")
		}
		for _, seg := range strings.Split(raw, "/") {
			s, err := cleanDeptName(seg)
			if err != nil {
				fail("dept_path 含非法部门名：" + err.Error())
				break
			}
			r.path = append(r.path, s)
		}
		if len(r.path) > maxDeptDepth {
			fail(fmt.Sprintf("部门层级最多 %d 级", maxDeptDepth))
		}
		rows = append(rows, r)
	}
	return rows, errs
}

type deptKey struct {
	parent int
	name   string
}

// importCSV POST /admin/departments/import[?dry_run=1]
// 按 dept_path（如 研发部/后端组）补齐缺失部门；用户按邮箱新建（无本地密码）或更新姓名 / 工号，并加入该部门。
func (h *deptHandlers) importCSV(c echo.Context) error {
	p := auth.Current(c)
	if _, orgWide := p.Perms.DeptRoots(rbac.DeptManage); !orgWide || !p.Perms.Has(rbac.MemberManage) {
		return authz.Deny(c, "import_scope")
	}
	ctx := c.Request().Context()
	b, err := readImportBody(c)
	if err != nil {
		return err
	}
	dry := c.QueryParam("dry_run") == "1" || c.QueryParam("dry_run") == "true"
	rows, errs := parseImport(b)
	res := ImportResult{DryRun: dry, Rows: len(rows), NewDepts: []string{}, Errors: errs}
	if res.Errors == nil {
		res.Errors = []importError{}
	}

	// 现有部门。
	ds, err := h.db.Department.Query().Where(department.OrgID(p.OrgID)).All(ctx)
	if err != nil {
		return err
	}
	known := map[deptKey]int{}
	for _, d := range ds {
		par := 0
		if d.ParentID != nil {
			par = *d.ParentID
		}
		known[deptKey{par, d.Name}] = d.ID
	}
	planned := map[string]bool{}
	for _, r := range rows {
		par, full := 0, ""
		for _, seg := range r.path {
			full = strings.TrimPrefix(full+"/"+seg, "/")
			if id, ok := known[deptKey{par, seg}]; ok && par >= 0 {
				par = id
				continue
			}
			par = -1 // 之后的层级都是新部门
			if !planned[full] {
				planned[full] = true
				res.NewDepts = append(res.NewDepts, full)
			}
		}
	}

	// 现有用户。
	emails := make([]string, 0, len(rows))
	for _, r := range rows {
		emails = append(emails, r.email)
	}
	users, err := h.db.User.Query().Where(user.EmailIn(emails...)).All(ctx)
	if err != nil {
		return err
	}
	byEmail := map[string]*ent.User{}
	for _, u := range users {
		byEmail[u.Email] = u
	}
	var nos []string
	for _, r := range rows {
		if r.employeeNo != "" {
			nos = append(nos, r.employeeNo)
		}
	}
	noOwner := map[string]string{}
	if len(nos) > 0 {
		us, err := h.db.User.Query().Where(user.OrgID(p.OrgID), user.EmployeeNoIn(nos...)).All(ctx)
		if err != nil {
			return err
		}
		for _, u := range us {
			noOwner[u.EmployeeNo] = u.Email
		}
	}
	for i := range rows {
		r := &rows[i]
		if u := byEmail[r.email]; u != nil {
			if u.OrgID != p.OrgID {
				res.Errors = append(res.Errors, importError{Line: r.line, Message: "该邮箱已被占用"})
				continue
			}
			r.existing = u
			res.UpdatedUsers++
		} else {
			res.CreatedUsers++
		}
		if r.employeeNo != "" {
			if owner, ok := noOwner[r.employeeNo]; ok && owner != r.email {
				res.Errors = append(res.Errors, importError{Line: r.line, Message: "工号已被其他成员占用"})
			}
		}
	}

	status := http.StatusOK
	if len(res.Errors) > 0 {
		status = http.StatusUnprocessableEntity
	}
	if dry || len(res.Errors) > 0 || len(rows) == 0 {
		return c.JSON(status, res)
	}

	// 先落审计（fail-closed），再在事务里写入。
	if err := audit.Record(c, audit.Entry{Action: "dept.import", Phase: "attempt", ResourceType: "org", ResourceID: strconv.Itoa(p.OrgID),
		Detail: map[string]any{"rows": res.Rows, "new_departments": len(res.NewDepts), "created_users": res.CreatedUsers, "updated_users": res.UpdatedUsers}}); err != nil {
		return err
	}
	err = withTx(ctx, h.db, func(tx *ent.Tx) error { return applyImport(ctx, tx, p.OrgID, rows, known) })
	if err != nil {
		if ent.IsConstraintError(err) {
			return echo.NewHTTPError(http.StatusConflict, "导入冲突（邮箱或工号已被占用），请刷新后重试")
		}
		return err
	}
	res.Applied = true
	return c.JSON(http.StatusOK, res)
}

func applyImport(ctx context.Context, tx *ent.Tx, orgID int, rows []importRow, known map[deptKey]int) error {
	mr, err := tx.Role.Query().Where(role.Key(rbac.Member)).OnlyID(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		par := 0
		for _, seg := range r.path {
			id, ok := known[deptKey{par, seg}]
			if !ok {
				var pp *int
				if par != 0 {
					pp = &par
				}
				d, err := store.CreateDept(ctx, tx, orgID, seg, pp)
				if err != nil {
					return err
				}
				id = d.ID
				known[deptKey{par, seg}] = id
			}
			par = id
		}
		var uid int
		if r.existing == nil {
			u, err := tx.User.Create().SetOrgID(orgID).SetEmail(r.email).SetDisplayName(r.name).SetEmployeeNo(r.employeeNo).Save(ctx)
			if err != nil {
				return err
			}
			uid = u.ID
		} else {
			uid = r.existing.ID
			up := tx.User.UpdateOneID(uid)
			if r.name != "" {
				up.SetDisplayName(r.name)
			}
			if r.employeeNo != "" {
				up.SetEmployeeNo(r.employeeNo)
			}
			if err := up.Exec(ctx); err != nil {
				return err
			}
			// 原来没有部门的「成员」关系替换为带部门的。
			if _, err := tx.Membership.Delete().Where(membership.UserID(uid), membership.RoleID(mr), membership.DepartmentIDIsNil()).Exec(ctx); err != nil {
				return err
			}
		}
		exists, err := tx.Membership.Query().Where(membership.UserID(uid), membership.RoleID(mr), membership.DepartmentID(par)).Exist(ctx)
		if err != nil {
			return err
		}
		if !exists {
			primary, err := tx.Membership.Query().Where(membership.UserID(uid), membership.IsPrimary(true)).Exist(ctx)
			if err != nil {
				return err
			}
			if err := tx.Membership.Create().SetUserID(uid).SetRoleID(mr).SetDepartmentID(par).SetIsPrimary(!primary).Exec(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}
