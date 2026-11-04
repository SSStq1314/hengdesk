package api

// 工具治理（M1 第五块 PR2）：风险等级 / 执行策略 / 审批人 / 审批超时 / 启用开关、待复核确认、工具 ACL；
// 员工侧「我可用的工具」列表。所有可用性判断都经 authz.ToolAccess（policy.Decide + use 位）。

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/acl"
	"github.com/SSStq1314/hengdesk/server/internal/ent/mcpserver"
	"github.com/SSStq1314/hengdesk/server/internal/ent/tool"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/policy"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

// 审批超时范围（秒）：1 分钟 – 7 天；默认 30 分钟（D-27：超时按拒绝）。
const (
	minApprovalTimeout = 60
	maxApprovalTimeout = 7 * 24 * 3600
	maxApprovers       = 20
)

type toolHandlers struct {
	db *ent.Client
	az *authz.Service
	md *modelHandlers // 复用主体显示名
}

func mountTools(g, admin *echo.Group, d Deps, az *authz.Service) {
	h := &toolHandlers{db: d.DB, az: az, md: &modelHandlers{db: d.DB}}
	need := authz.Require(rbac.ToolManage)
	admin.GET("/tools", h.list, need)
	admin.GET("/tools/:id", h.get, need)
	admin.PATCH("/tools/:id", h.update, need)
	admin.GET("/tools/:id/acl", h.listACL, need)
	admin.POST("/tools/:id/acl", h.grantACL, need)
	admin.DELETE("/tools/:id/acl/:type/:sid", h.revokeACL, need)
	g.GET("/tools", h.mine)
}

// load 本组织的工具；其他组织 / 不存在一律 404。
func (h *toolHandlers) load(c echo.Context) (*ent.Tool, error) {
	id, err := idParam(c)
	if err != nil {
		return nil, err
	}
	p := auth.Current(c)
	t, err := h.db.Tool.Query().Where(tool.ID(id), tool.OrgID(p.OrgID)).Only(c.Request().Context())
	if err != nil {
		return nil, notFoundOr(err)
	}
	return t, nil
}

type adminToolView struct {
	toolView
	ServerEnabled bool          `json:"server_enabled"`
	Approvers     []subjectInfo `json:"approvers"`
	GrantCount    int           `json:"grant_count"`
}

type subjectInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (h *toolHandlers) views(ctx context.Context, orgID int, ts []*ent.Tool) ([]adminToolView, error) {
	srv := map[int]*ent.McpServer{}
	ss, err := h.db.McpServer.Query().Where(mcpserver.OrgID(orgID)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range ss {
		srv[s.ID] = s
	}
	keys := make([]string, len(ts))
	for i, t := range ts {
		keys[i] = t.Key
	}
	grants := map[string]int{}
	if len(keys) > 0 {
		rows, err := h.db.ACL.Query().Where(acl.OrgID(orgID), acl.ResourceType(rbac.ResTool), acl.ResourceIDIn(keys...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			grants[r.ResourceID]++
		}
	}
	out := make([]adminToolView, len(ts))
	for i, t := range ts {
		v := adminToolView{toolView: toToolView(t), GrantCount: grants[t.Key], Approvers: []subjectInfo{}}
		if s := srv[v.ServerID]; s != nil {
			v.ServerName, v.ServerEnabled = s.Name, s.Enabled
		}
		for _, uid := range t.ApproverIds {
			v.Approvers = append(v.Approvers, subjectInfo{ID: uid, Name: h.md.subjectName(ctx, orgID, "user", uid)})
		}
		out[i] = v
	}
	return out, nil
}

// list GET /admin/tools?server_id=&needs_review=1
func (h *toolHandlers) list(c echo.Context) error {
	ctx := c.Request().Context()
	p := auth.Current(c)
	q := h.db.Tool.Query().Where(tool.OrgID(p.OrgID))
	if v := c.QueryParam("server_id"); v != "" {
		id, err := strconv.Atoi(v)
		if err != nil {
			return bad("server_id 不合法")
		}
		q = q.Where(tool.ServerID(id))
	}
	if c.QueryParam("needs_review") == "1" {
		q = q.Where(tool.NeedsReview(true))
	}
	ts, err := q.Order(ent.Asc(tool.FieldServerID), ent.Asc(tool.FieldName)).All(ctx)
	if err != nil {
		return err
	}
	out, err := h.views(ctx, p.OrgID, ts)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out})
}

func (h *toolHandlers) get(c echo.Context) error {
	t, err := h.load(c)
	if err != nil {
		return err
	}
	out, err := h.views(c.Request().Context(), auth.Current(c).OrgID, []*ent.Tool{t})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out[0])
}

type toolInput struct {
	Enabled            *bool   `json:"enabled"`
	ExecMode           *string `json:"exec_mode"`
	RiskLevel          *string `json:"risk_level"`
	ApproverIDs        *[]int  `json:"approver_ids"`
	ApprovalTimeoutSec *int    `json:"approval_timeout_sec"`
	// ConfirmReview 管理员确认已复核（schema 变化 / 重新上线）；确认后才能再次启用。
	ConfirmReview bool `json:"confirm_review"`
}

// update PATCH /admin/tools/:id
func (h *toolHandlers) update(c echo.Context) error {
	t, err := h.load(c)
	if err != nil {
		return err
	}
	var in toolInput
	if err := c.Bind(&in); err != nil {
		return bad("请求格式错误")
	}
	ctx := c.Request().Context()
	p := auth.Current(c)
	risk, mode := string(t.RiskLevel), string(t.ExecMode)
	if in.RiskLevel != nil {
		risk = *in.RiskLevel
	}
	if in.ExecMode != nil {
		mode = *in.ExecMode
	}
	if msg := policy.ValidateSettings(risk, mode); msg != "" {
		return bad(msg)
	}
	review := t.NeedsReview && !in.ConfirmReview
	enabled := t.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	if in.Enabled != nil && *in.Enabled {
		switch {
		case t.Removed:
			return conflict("工具已在服务端下线，不能启用")
		case review:
			return conflict("工具定义有变化，请先确认复核后再启用")
		}
		if t.ServerID != nil {
			s, err := h.db.McpServer.Get(ctx, *t.ServerID)
			if err != nil || !s.Enabled {
				return conflict("所属 MCP 服务器已停用，不能启用工具")
			}
		}
	}
	u := t.Update().SetRiskLevel(tool.RiskLevel(risk)).SetExecMode(tool.ExecMode(mode)).SetEnabled(enabled)
	if in.ConfirmReview && t.NeedsReview {
		u.SetNeedsReview(false)
	}
	if in.ApproverIDs != nil {
		ids, err := h.checkApprovers(ctx, p.OrgID, *in.ApproverIDs)
		if err != nil {
			return err
		}
		u.SetApproverIds(ids)
	}
	if in.ApprovalTimeoutSec != nil {
		v := *in.ApprovalTimeoutSec
		if v < minApprovalTimeout || v > maxApprovalTimeout {
			return bad("审批超时须在 60 秒到 7 天之间")
		}
		u.SetApprovalTimeoutSec(v)
	}
	nt, err := u.Save(ctx)
	if err != nil {
		return err
	}
	detail := map[string]any{"key": t.Key, "name": t.Name}
	diff := func(k string, a, b any) {
		if a != b {
			detail[k] = map[string]any{"from": a, "to": b}
		}
	}
	diff("enabled", t.Enabled, nt.Enabled)
	diff("exec_mode", string(t.ExecMode), string(nt.ExecMode))
	diff("risk_level", string(t.RiskLevel), string(nt.RiskLevel))
	diff("approval_timeout_sec", t.ApprovalTimeoutSec, nt.ApprovalTimeoutSec)
	if in.ApproverIDs != nil {
		detail["approver_ids"] = nt.ApproverIds
	}
	if in.ConfirmReview && t.NeedsReview {
		detail["review_confirmed"] = true
	}
	if err := h.md.record(c, "tool.update", "tool", t.ID, detail); err != nil {
		return err
	}
	out, err := h.views(ctx, p.OrgID, []*ent.Tool{nt})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out[0])
}

// checkApprovers 审批人必须是本组织的在职成员，去重排序；空列表表示由工具管理员（有 tool.manage 的人）审批。
func (h *toolHandlers) checkApprovers(ctx context.Context, orgID int, ids []int) ([]int, error) {
	if len(ids) > maxApprovers {
		return nil, bad("审批人最多 20 个")
	}
	seen := map[int]bool{}
	out := []int{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Ints(out)
	if len(out) == 0 {
		return out, nil
	}
	n, err := h.db.User.Query().Where(user.IDIn(out...), user.OrgID(orgID), user.StatusEQ(user.StatusActive)).Count(ctx)
	if err != nil {
		return nil, err
	}
	if n != len(out) {
		return nil, bad("审批人必须是本组织的在职成员")
	}
	return out, nil
}

func (h *toolHandlers) listACL(c echo.Context) error {
	t, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	p := auth.Current(c)
	rows, err := h.db.ACL.Query().Where(acl.OrgID(p.OrgID), acl.ResourceType(rbac.ResTool), acl.ResourceID(t.Key)).
		Order(ent.Asc(acl.FieldID)).All(ctx)
	if err != nil {
		return err
	}
	out := make([]aclView, 0, len(rows))
	for _, r := range rows {
		out = append(out, aclView{SubjectType: string(r.SubjectType), SubjectID: r.SubjectID,
			SubjectName:     h.md.subjectName(ctx, p.OrgID, string(r.SubjectType), r.SubjectID),
			IncludeChildren: r.IncludeChildren, Perms: r.Perms})
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out})
}

func (h *toolHandlers) grantACL(c echo.Context) error {
	t, err := h.load(c)
	if err != nil {
		return err
	}
	var in aclInput
	if err := c.Bind(&in); err != nil {
		return bad("请求格式错误")
	}
	return h.setACL(c, t, in.SubjectType, in.SubjectID, in.IncludeChildren, rbac.BitUse, "tool.acl.grant")
}

func (h *toolHandlers) revokeACL(c echo.Context) error {
	t, err := h.load(c)
	if err != nil {
		return err
	}
	sid, err := strconv.Atoi(c.Param("sid"))
	if err != nil {
		return authz.ErrNotFound
	}
	return h.setACL(c, t, c.Param("type"), sid, false, 0, "tool.acl.revoke")
}

func (h *toolHandlers) setACL(c echo.Context, t *ent.Tool, typ string, sid int, children bool, bits int, action string) error {
	ctx := c.Request().Context()
	p := auth.Current(c)
	if typ != "user" && typ != "dept" && typ != "role" {
		return bad("subject_type 只能是 user / dept / role")
	}
	name := h.md.subjectName(ctx, p.OrgID, typ, sid)
	if name == "" && bits != 0 {
		return bad("授权对象不存在")
	}
	if err := h.az.Grant(ctx, p.OrgID, p.ID, rbac.ResTool, t.Key,
		authz.Subject{Type: typ, ID: sid, IncludeChildren: children}, bits); err != nil {
		if errors.Is(err, authz.ErrBadSubject) {
			return bad("授权对象不合法")
		}
		return err
	}
	if err := h.md.record(c, action, "tool", t.ID, map[string]any{"key": t.Key, "name": t.Name, "subject_type": typ,
		"subject_id": sid, "subject_name": name, "include_children": children && typ == "dept"}); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// UsableTool 员工可见的工具（不含审批人、授权等治理信息）。
type UsableTool struct {
	ID          int            `json:"id"`
	Key         string         `json:"key"`
	Name        string         `json:"name"`
	ServerName  string         `json:"server_name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
	RiskLevel   string         `json:"risk_level"`
	ExecMode    string         `json:"exec_mode"`
}

// UsableTools 当前用户可用的工具（经 authz.ToolAccess），按服务器名、工具名排序。
func UsableTools(ctx context.Context, db *ent.Client, az *authz.Service, p *auth.Principal) ([]UsableTool, error) {
	ts, err := db.Tool.Query().Where(tool.OrgID(p.OrgID), tool.Enabled(true), tool.Removed(false), tool.NeedsReview(false),
		tool.ExecModeNEQ(tool.ExecModeDeny)).All(ctx)
	if err != nil {
		return nil, err
	}
	names := map[int]string{}
	out := []UsableTool{}
	for _, t := range ts {
		d, err := az.ToolAccess(ctx, p, t)
		if err != nil {
			return nil, err
		}
		if !d.Usable {
			continue
		}
		v := UsableTool{ID: t.ID, Key: t.Key, Name: t.Name, Description: t.Description, InputSchema: t.InputSchema,
			RiskLevel: string(t.RiskLevel), ExecMode: d.Mode}
		if t.ServerID != nil {
			if _, ok := names[*t.ServerID]; !ok {
				if s, err := db.McpServer.Get(ctx, *t.ServerID); err == nil {
					names[*t.ServerID] = s.Name
				}
			}
			v.ServerName = names[*t.ServerID]
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ServerName != out[j].ServerName {
			return out[i].ServerName < out[j].ServerName
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// mine GET /tools：我可用的工具。
func (h *toolHandlers) mine(c echo.Context) error {
	p := auth.Current(c)
	if p == nil {
		return authz.ErrUnauthenticated
	}
	out, err := UsableTools(c.Request().Context(), h.db, h.az, p)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out})
}

func conflict(msg string) error { return echo.NewHTTPError(http.StatusConflict, msg) }
