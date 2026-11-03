package api

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/department"
	"github.com/SSStq1314/hengdesk/server/internal/ent/membership"
	"github.com/SSStq1314/hengdesk/server/internal/ent/role"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

type memberHandlers struct {
	db   *ent.Client
	az   *authz.Service
	auth *auth.Service
}

type deptRef struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type roleRef struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	DepartmentID *int   `json:"department_id,omitempty"`
}

type memberView struct {
	ID          int        `json:"id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"display_name"`
	EmployeeNo  string     `json:"employee_no"`
	Status      string     `json:"status"`
	HasPassword bool       `json:"has_password"`
	LastLoginAt *time.Time `json:"last_login_at"`
	Departments []deptRef  `json:"departments"`
	Roles       []roleRef  `json:"roles"`
}

func viewOf(u *ent.User) memberView {
	v := memberView{ID: u.ID, Email: u.Email, DisplayName: u.DisplayName, EmployeeNo: u.EmployeeNo,
		Status: string(u.Status), HasPassword: u.PasswordHash != "", LastLoginAt: u.LastLoginAt,
		Departments: []deptRef{}, Roles: []roleRef{}}
	seen := map[int]bool{}
	for _, m := range u.Edges.Memberships {
		if m.Edges.Department != nil && !seen[m.Edges.Department.ID] {
			seen[m.Edges.Department.ID] = true
			v.Departments = append(v.Departments, deptRef{ID: m.Edges.Department.ID, Name: m.Edges.Department.Name})
		}
		if r := m.Edges.Role; r != nil && r.Key != rbac.Member {
			name := r.Key
			if b, ok := rbac.Lookup(r.Key); ok {
				name = b.Name
			}
			v.Roles = append(v.Roles, roleRef{Key: r.Key, Name: name, DepartmentID: m.DepartmentID})
		}
	}
	return v
}

func withMemberships(q *ent.MembershipQuery) { q.WithDepartment().WithRole() }

// list GET /admin/users?q=&dept_id=&status=&limit=&before_id=
func (h *memberHandlers) list(c echo.Context) error {
	p := auth.Current(c)
	ctx := c.Request().Context()
	scope, ok, err := h.az.UserScope(ctx, p, rbac.MemberRead)
	if err != nil {
		return err
	}
	if !ok {
		return authz.Deny(c, "scope")
	}
	q := h.db.User.Query().Where(user.OrgID(p.OrgID))
	for _, f := range scope {
		f(q)
	}
	if s := strings.TrimSpace(c.QueryParam("q")); s != "" {
		q.Where(user.Or(user.EmailContainsFold(s), user.DisplayNameContainsFold(s), user.EmployeeNo(s)))
	}
	if st := c.QueryParam("status"); st == "active" || st == "disabled" {
		q.Where(user.StatusEQ(user.Status(st)))
	}
	if d, _ := strconv.Atoi(c.QueryParam("dept_id")); d > 0 {
		tree, err := h.az.Subtree(ctx, p.OrgID, []int{d})
		if err != nil {
			return err
		}
		q.Where(user.HasMembershipsWith(membership.DepartmentIDIn(tree...)))
	}
	if b, _ := strconv.Atoi(c.QueryParam("before_id")); b > 0 {
		q.Where(user.IDLT(b))
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	us, err := q.WithMemberships(withMemberships).Order(ent.Desc(user.FieldID)).Limit(limit + 1).All(ctx)
	if err != nil {
		return err
	}
	next := 0
	if len(us) > limit {
		us = us[:limit]
		next = us[limit-1].ID
	}
	items := make([]memberView, 0, len(us))
	for _, u := range us {
		items = append(items, viewOf(u))
	}
	return c.JSON(http.StatusOK, map[string]any{"items": items, "next_before_id": next})
}

// get GET /admin/users/:id
func (h *memberHandlers) get(c echo.Context) error {
	id, _ := strconv.Atoi(c.Param("id"))
	u, err := h.az.VisibleUser(c, rbac.MemberRead, id)
	if err != nil {
		return err
	}
	u, err = h.db.User.Query().Where(user.ID(u.ID)).WithMemberships(withMemberships).Only(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, viewOf(u))
}

type memberReq struct {
	Email         string  `json:"email"`
	DisplayName   *string `json:"display_name"`
	EmployeeNo    *string `json:"employee_no"`
	DepartmentIDs *[]int  `json:"department_ids"`
	Password      string  `json:"password"`
}

// checkDepts 部门必须属于本组织且在操作者 perm 范围内（部门范围权限只能落到子树内）。
func (h *memberHandlers) checkDepts(c echo.Context, perm rbac.Perm, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	p := auth.Current(c)
	ctx := c.Request().Context()
	n, err := h.db.Department.Query().Where(department.OrgID(p.OrgID), department.IDIn(ids...)).Count(ctx)
	if err != nil {
		return err
	}
	if n != len(uniq(ids)) {
		return authz.Deny(c, "idor:dept")
	}
	roots, orgWide := p.Perms.DeptRoots(perm)
	if orgWide {
		return nil
	}
	tree, err := h.az.Subtree(ctx, p.OrgID, roots)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if !slices.Contains(tree, id) {
			return authz.Deny(c, "dept_scope")
		}
	}
	return nil
}

func uniq(v []int) []int {
	s := slices.Clone(v)
	slices.Sort(s)
	return slices.Compact(s)
}

func normEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func validEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	return at > 0 && at < len(s)-1 && !strings.ContainsAny(s, " \t\r\n,;") && strings.Count(s, "@") == 1
}

// setDepts 用「成员」角色的成员关系表达部门归属：替换为 ids。
func setDepts(ctx context.Context, tx *ent.Tx, uid int, ids []int) error {
	mr, err := tx.Role.Query().Where(role.Key(rbac.Member)).OnlyID(ctx)
	if err != nil {
		return err
	}
	if _, err := tx.Membership.Delete().Where(membership.UserID(uid), membership.RoleID(mr)).Exec(ctx); err != nil {
		return err
	}
	ids = uniq(ids)
	if len(ids) == 0 {
		return tx.Membership.Create().SetUserID(uid).SetRoleID(mr).Exec(ctx)
	}
	bulk := make([]*ent.MembershipCreate, 0, len(ids))
	for i, d := range ids {
		bulk = append(bulk, tx.Membership.Create().SetUserID(uid).SetRoleID(mr).SetDepartmentID(d).SetIsPrimary(i == 0))
	}
	return tx.Membership.CreateBulk(bulk...).Exec(ctx)
}

func withTx(ctx context.Context, db *ent.Client, fn func(tx *ent.Tx) error) error {
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

var errEmployeeNoTaken = echo.NewHTTPError(http.StatusConflict, "工号已被占用")

// create POST /admin/users
func (h *memberHandlers) create(c echo.Context) error {
	var req memberReq
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "请求格式错误")
	}
	p := auth.Current(c)
	ctx := c.Request().Context()
	email := normEmail(req.Email)
	if !validEmail(email) {
		return echo.NewHTTPError(http.StatusBadRequest, "邮箱格式不正确")
	}
	var depts []int
	if req.DepartmentIDs != nil {
		depts = *req.DepartmentIDs
	}
	if err := h.checkDepts(c, rbac.MemberManage, depts); err != nil {
		return err
	}
	if req.Password != "" {
		if err := h.auth.Passwords.Validate(req.Password, email); err != nil {
			return err
		}
	}
	if exists, err := h.db.User.Query().Where(user.Email(email)).Exist(ctx); err != nil {
		return err
	} else if exists {
		// 邮箱全局唯一；不区分本组织 / 他组织，避免探测。
		return echo.NewHTTPError(http.StatusConflict, "该邮箱已存在")
	}
	var uid int
	err := withTx(ctx, h.db, func(tx *ent.Tx) error {
		cr := tx.User.Create().SetEmail(email).SetOrgID(p.OrgID)
		if req.DisplayName != nil {
			cr.SetDisplayName(strings.TrimSpace(*req.DisplayName))
		}
		if req.EmployeeNo != nil {
			cr.SetEmployeeNo(strings.TrimSpace(*req.EmployeeNo))
		}
		u, err := cr.Save(ctx)
		if err != nil {
			if ent.IsConstraintError(err) {
				return errEmployeeNoTaken
			}
			return err
		}
		uid = u.ID
		return setDepts(ctx, tx, u.ID, depts)
	})
	if err != nil {
		return err
	}
	if req.Password != "" {
		if err := h.auth.SetPassword(c, uid, req.Password); err != nil {
			return err
		}
	}
	u, err := h.db.User.Query().Where(user.ID(uid)).WithMemberships(withMemberships).Only(ctx)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, viewOf(u))
}

// update PATCH /admin/users/:id（姓名、工号、部门）
func (h *memberHandlers) update(c echo.Context) error {
	u, err := h.auth.Target(c)
	if err != nil {
		return err
	}
	var req memberReq
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "请求格式错误")
	}
	if req.DepartmentIDs != nil {
		if err := h.checkDepts(c, rbac.MemberManage, *req.DepartmentIDs); err != nil {
			return err
		}
	}
	ctx := c.Request().Context()
	err = withTx(ctx, h.db, func(tx *ent.Tx) error {
		up := tx.User.UpdateOneID(u.ID)
		if req.DisplayName != nil {
			up.SetDisplayName(strings.TrimSpace(*req.DisplayName))
		}
		if req.EmployeeNo != nil {
			up.SetEmployeeNo(strings.TrimSpace(*req.EmployeeNo))
		}
		if err := up.Exec(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return errEmployeeNoTaken
			}
			return err
		}
		if req.DepartmentIDs != nil {
			return setDepts(ctx, tx, u.ID, *req.DepartmentIDs)
		}
		return nil
	})
	if err != nil {
		return err
	}
	u, err = h.db.User.Query().Where(user.ID(u.ID)).WithMemberships(withMemberships).Only(ctx)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, viewOf(u))
}
