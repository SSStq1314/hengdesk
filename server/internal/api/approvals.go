package api

// 审批中心（M1 第五块 PR3）：待我审批 / 我发起的 / 详情 / 通过 / 拒绝 / 撤回。
// 只有发起人和审批人能看到单子，其余一律 404；发起人不能审批自己的单子（403）。

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/approval"
	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/egress"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	entapproval "github.com/SSStq1314/hengdesk/server/internal/ent/approval"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/policy"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

// mcpCaller 用 MCP 客户端实现 approval.Caller（出站标记 module=mcp）。
type mcpCaller struct{ h *mcpHandlers }

func (m mcpCaller) Call(ctx context.Context, t *ent.Tool, s *ent.McpServer, args map[string]any) (string, bool, error) {
	if s == nil {
		return "", false, errors.New("工具所属的 MCP 服务器不存在")
	}
	sv, err := m.h.connParams(s)
	if err != nil {
		return "", false, err
	}
	if egress.CallerFrom(ctx).Module == "" {
		ctx = egress.WithCaller(ctx, egress.Caller{Module: "mcp"})
	}
	name := t.Name
	res, err := m.h.cli.CallTool(ctx, sv, name, args)
	if err != nil {
		return "", false, errors.New(mcpErrorText(err, headerValues(sv.Headers)...))
	}
	return res.Text, res.IsError, nil
}

func newApprovalService(d Deps, mh *mcpHandlers, az *authz.Service) *approval.Service {
	s := &approval.Service{DB: d.DB, Audit: d.Audit, Caller: mcpCaller{h: mh}, Log: d.Log,
		Access: requesterAccess(d.DB, az), Managers: toolManagers(d.DB)}
	if d.ContinueEnqueue != nil {
		lg := d.Log
		s.OnResolved = func(ctx context.Context, ap *ent.Approval) {
			if ap.MessageID == nil {
				return
			}
			// 投递失败只记日志：定时补投（approval:continue_sweep）会再投一次。
			if err := d.ContinueEnqueue(ctx, *ap.MessageID); err != nil && lg != nil {
				lg.Warn("enqueue approval continue failed", "approval", ap.ID, "message", *ap.MessageID, "err", err)
			}
		}
	}
	return s
}

// requesterAccess 按发起人当前的权限现算工具是否可用（策略 + use 授权）；账号已停用 / 删除视为无权限。
func requesterAccess(db *ent.Client, az *authz.Service) func(ctx context.Context, orgID, userID int, t *ent.Tool) (policy.Decision, error) {
	return func(ctx context.Context, orgID, userID int, t *ent.Tool) (policy.Decision, error) {
		p, err := auth.LoadPrincipal(ctx, db, userID)
		if ent.IsNotFound(err) {
			return policy.Decision{Reason: "requester_inactive"}, nil
		}
		if err != nil {
			return policy.Decision{}, err
		}
		if p.OrgID != orgID {
			return policy.Decision{Reason: "requester_inactive"}, nil
		}
		d, err := az.ToolAccess(ctx, p, t)
		if err == nil && !d.Usable && d.Reason == "" {
			d.Reason = "no_permission"
		}
		return d, err
	}
}

// toolManagers 组织内所有在职、拥有组织范围 tool.manage 的用户（审批人为空时由他们审批）。
func toolManagers(db *ent.Client) func(ctx context.Context, orgID int) ([]int, error) {
	return func(ctx context.Context, orgID int) ([]int, error) {
		ids, err := db.User.Query().Where(user.OrgID(orgID), user.StatusEQ(user.StatusActive)).Order(ent.Asc(user.FieldID)).Limit(5000).IDs(ctx)
		if err != nil {
			return nil, err
		}
		var out []int
		for _, id := range ids {
			p, err := auth.LoadPrincipal(ctx, db, id)
			if ent.IsNotFound(err) {
				continue
			}
			if err != nil {
				return out, err
			}
			if p.Perms.Has(rbac.ToolManage) {
				out = append(out, id)
			}
		}
		return out, nil
	}
}

type approvalHandlers struct {
	db  *ent.Client
	svc *approval.Service
}

func mountApprovals(g *echo.Group, svc *approval.Service) {
	h := &approvalHandlers{db: svc.DB, svc: svc}
	g.GET("/approvals", h.list)
	g.GET("/approvals/:id", h.get)
	g.POST("/approvals/:id/approve", h.approve)
	g.POST("/approvals/:id/reject", h.reject)
	g.POST("/approvals/:id/withdraw", h.withdraw)
}

func actorOf(p *auth.Principal) approval.Actor {
	return approval.Actor{OrgID: p.OrgID, ID: p.ID, Email: p.Email, CanManage: p.Perms.Has(rbac.ToolManage)}
}

type approvalView struct {
	ID             int            `json:"id"`
	ToolID         int            `json:"tool_id"`
	ToolKey        string         `json:"tool_key"`
	ToolName       string         `json:"tool_name"`
	RiskLevel      string         `json:"risk_level"`
	Requester      subjectInfo    `json:"requester"`
	ConversationID *int           `json:"conversation_id"`
	Args           map[string]any `json:"args"`
	ContextSummary string         `json:"context_summary"`
	Approvers      []subjectInfo  `json:"approvers"`
	AnyAdmin       bool           `json:"approver_any_admin"`
	Status         string         `json:"status"`
	DecidedBy      *subjectInfo   `json:"decided_by"`
	Comment        string         `json:"decision_comment"`
	DecidedAt      *time.Time     `json:"decided_at"`
	ExpiresAt      time.Time      `json:"expires_at"`
	ExecStatus     string         `json:"exec_status"`
	Result         string         `json:"result"`
	ResultIsError  bool           `json:"result_is_error"`
	TraceID        string         `json:"trace_id"`
	CreatedAt      time.Time      `json:"created_at"`
	CanDecide      bool           `json:"can_decide"`
	CanWithdraw    bool           `json:"can_withdraw"`
}

func (h *approvalHandlers) views(ctx context.Context, a approval.Actor, aps []*ent.Approval) ([]approvalView, error) {
	ids := map[int]bool{}
	for _, ap := range aps {
		ids[ap.RequesterID] = true
		for _, id := range ap.ApproverIds {
			ids[id] = true
		}
		if ap.DecidedBy != nil {
			ids[*ap.DecidedBy] = true
		}
	}
	keys := make([]int, 0, len(ids))
	for id := range ids {
		keys = append(keys, id)
	}
	us, err := h.db.User.Query().Where(user.IDIn(keys...), user.OrgID(a.OrgID)).All(ctx)
	if err != nil {
		return nil, err
	}
	names := map[int]string{}
	for _, u := range us {
		n := u.DisplayName
		if n == "" {
			n = u.Email
		}
		names[u.ID] = n
	}
	si := func(id int) subjectInfo { return subjectInfo{ID: id, Name: names[id]} }
	out := make([]approvalView, 0, len(aps))
	for _, ap := range aps {
		v := approvalView{ID: ap.ID, ToolID: ap.ToolID, ToolKey: ap.ToolKey, ToolName: ap.ToolName, RiskLevel: ap.RiskLevel,
			Requester: si(ap.RequesterID), ConversationID: ap.ConversationID, Args: ap.Args, ContextSummary: ap.ContextSummary,
			Approvers: []subjectInfo{}, AnyAdmin: len(ap.ApproverIds) == 0, Status: string(ap.Status), Comment: ap.DecisionComment,
			DecidedAt: ap.DecidedAt, ExpiresAt: ap.ExpiresAt, ExecStatus: string(ap.ExecStatus), Result: ap.Result,
			ResultIsError: ap.ResultIsError, TraceID: ap.TraceID, CreatedAt: ap.CreatedAt}
		if v.Args == nil {
			v.Args = map[string]any{}
		}
		for _, id := range ap.ApproverIds {
			v.Approvers = append(v.Approvers, si(id))
		}
		if ap.DecidedBy != nil {
			d := si(*ap.DecidedBy)
			v.DecidedBy = &d
		}
		pending := ap.Status == entapproval.StatusPending
		v.CanDecide = pending && ap.RequesterID != a.ID && approval.IsApprover(a, ap)
		v.CanWithdraw = pending && ap.RequesterID == a.ID
		out = append(out, v)
	}
	return out, nil
}

// list ?box=todo（待我审批，默认）| mine（我发起的）；?status=pending|done|all（todo 默认 pending，mine 默认 all）。
func (h *approvalHandlers) list(c echo.Context) error {
	p := auth.Current(c)
	if p == nil {
		return echo.ErrUnauthorized
	}
	ctx := c.Request().Context()
	if _, err := h.svc.ExpireDue(ctx); err != nil {
		return err
	}
	a := actorOf(p)
	box := c.QueryParam("box")
	if box == "" {
		box = "todo"
	}
	status := c.QueryParam("status")
	q := h.db.Approval.Query().Where(entapproval.OrgID(p.OrgID))
	switch box {
	case "mine":
		q = q.Where(entapproval.RequesterID(p.ID))
	case "todo":
		q = q.Where(entapproval.RequesterIDNEQ(p.ID))
		if status == "" {
			status = "pending"
		}
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "box 只能是 todo 或 mine")
	}
	switch status {
	case "", "all":
	case "pending":
		q = q.Where(entapproval.StatusEQ(entapproval.StatusPending))
	case "done":
		q = q.Where(entapproval.StatusNEQ(entapproval.StatusPending))
	default:
		return echo.NewHTTPError(http.StatusBadRequest, "status 只能是 pending、done 或 all")
	}
	aps, err := q.Order(ent.Desc(entapproval.FieldID)).Limit(500).All(ctx)
	if err != nil {
		return err
	}
	if box == "todo" {
		aps = slices.DeleteFunc(aps, func(ap *ent.Approval) bool { return !approval.IsApprover(a, ap) })
	}
	if len(aps) > 200 {
		aps = aps[:200]
	}
	vs, err := h.views(ctx, a, aps)
	if err != nil {
		return err
	}
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].ID > vs[j].ID })
	return c.JSON(http.StatusOK, map[string]any{"items": vs})
}

func approvalErr(err error) error {
	switch {
	case errors.Is(err, approval.ErrNotFound):
		return echo.ErrNotFound
	case errors.Is(err, approval.ErrSelfApproval):
		return echo.NewHTTPError(http.StatusForbidden, approval.ErrSelfApproval.Error())
	case errors.Is(err, approval.ErrNotPending):
		return conflict(approval.ErrNotPending.Error())
	case errors.Is(err, approval.ErrAudit):
		return audit.ErrRejected
	}
	return err
}

func (h *approvalHandlers) one(c echo.Context, ap *ent.Approval) error {
	vs, err := h.views(c.Request().Context(), actorOf(auth.Current(c)), []*ent.Approval{ap})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, vs[0])
}

func (h *approvalHandlers) get(c echo.Context) error {
	p := auth.Current(c)
	if p == nil {
		return echo.ErrUnauthorized
	}
	id, err := idParam(c)
	if err != nil {
		return err
	}
	ap, err := h.svc.Get(c.Request().Context(), actorOf(p), id)
	if err != nil {
		return approvalErr(err)
	}
	return h.one(c, ap)
}

func (h *approvalHandlers) decide(c echo.Context, ok bool) error {
	p := auth.Current(c)
	if p == nil {
		return echo.ErrUnauthorized
	}
	id, err := idParam(c)
	if err != nil {
		return err
	}
	var in struct {
		Comment string `json:"comment"`
	}
	if err := c.Bind(&in); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "请求格式错误")
	}
	ap, err := h.svc.Decide(egressCtx(c.Request().Context(), c, "mcp"), actorOf(p), id, ok, in.Comment)
	if err != nil {
		return approvalErr(err)
	}
	return h.one(c, ap)
}

func (h *approvalHandlers) approve(c echo.Context) error { return h.decide(c, true) }
func (h *approvalHandlers) reject(c echo.Context) error  { return h.decide(c, false) }

func (h *approvalHandlers) withdraw(c echo.Context) error {
	p := auth.Current(c)
	if p == nil {
		return echo.ErrUnauthorized
	}
	id, err := idParam(c)
	if err != nil {
		return err
	}
	ap, err := h.svc.Withdraw(c.Request().Context(), actorOf(p), id)
	if err != nil {
		return approvalErr(err)
	}
	return h.one(c, ap)
}
