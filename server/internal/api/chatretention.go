package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/chatretention"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/legalhold"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
)

// retentionHandlers 对话保留期与法律保留（FR-40，D-40 第 2 条）。仅系统设置权限（超级管理员）。
type retentionHandlers struct {
	db  *ent.Client
	fs  FileStore
	now func() time.Time
}

func (h *retentionHandlers) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

func (h *retentionHandlers) get(c echo.Context) error {
	st, err := chatretention.Load(c.Request().Context(), h.db)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"settings": st, "default_days": chatretention.DefaultDays, "min_days": chatretention.MinDays})
}

func (h *retentionHandlers) put(c echo.Context) error {
	var st chatretention.Settings
	if err := c.Bind(&st); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "请求格式错误")
	}
	if st.Days == 0 {
		st.Days = chatretention.DefaultDays
	}
	if err := st.Validate(); err != nil {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
	}
	ctx := c.Request().Context()
	old, err := chatretention.Load(ctx, h.db)
	if err != nil {
		return err
	}
	if err := audit.Record(c, audit.Entry{Action: "chat.retention.update", Phase: "attempt", ResourceType: "system_setting",
		ResourceID: chatretention.SettingKey, Detail: map[string]any{"enabled": st.Enabled, "days": st.Days,
			"old_enabled": old.Enabled, "old_days": old.Days}}); err != nil {
		return err
	}
	if err := chatretention.Save(ctx, h.db, st, auth.Current(c).ID); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"settings": st})
}

// run POST /admin/chat-retention/run：立即执行一次清理（与定时任务相同逻辑，操作人为当前用户）。
func (h *retentionHandlers) run(c echo.Context) error {
	p := auth.Current(c)
	var objs chatretention.ObjectDeleter
	if h.fs != nil {
		objs = h.fs
	}
	r := c.Request()
	res, err := chatretention.Purge(r.Context(), h.db, audit.EntWriterFrom(c), objs, chatretention.Operator{
		OrgID: p.OrgID, ActorID: p.ID, Email: p.Email, Trigger: "manual", IP: c.RealIP(), UA: r.UserAgent(),
		Request: c.Response().Header().Get(echo.HeaderXRequestID), Trace: audit.TraceFrom(r.Context())}, h.clock())
	if errors.Is(err, chatretention.ErrDisabled) {
		return echo.NewHTTPError(http.StatusConflict, "未开启对话保留期清理")
	}
	if err != nil {
		if errors.Is(err, chatretention.ErrAudit) {
			return audit.ErrRejected
		}
		return err
	}
	return c.JSON(http.StatusOK, res)
}

type holdView struct {
	ID         int        `json:"id"`
	Scope      string     `json:"scope"` // org / user
	UserID     *int       `json:"user_id,omitempty"`
	UserEmail  string     `json:"user_email,omitempty"`
	Reason     string     `json:"reason"`
	CreatedBy  int        `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	ReleasedAt *time.Time `json:"released_at,omitempty"`
	ReleasedBy *int       `json:"released_by,omitempty"`
}

func (h *retentionHandlers) listHolds(c echo.Context) error {
	ctx := c.Request().Context()
	p := auth.Current(c)
	rows, err := h.db.LegalHold.Query().Where(legalhold.OrgID(p.OrgID)).Order(ent.Desc(legalhold.FieldID)).Limit(500).All(ctx)
	if err != nil {
		return err
	}
	var uids []int
	for _, r := range rows {
		if r.UserID != nil {
			uids = append(uids, *r.UserID)
		}
	}
	emails := map[int]string{}
	if len(uids) > 0 {
		us, err := h.db.User.Query().Where(user.IDIn(uids...)).All(ctx)
		if err != nil {
			return err
		}
		for _, u := range us {
			emails[u.ID] = u.Email
		}
	}
	out := make([]holdView, 0, len(rows))
	for _, r := range rows {
		v := holdView{ID: r.ID, Scope: "org", UserID: r.UserID, Reason: r.Reason, CreatedBy: r.CreatedBy,
			CreatedAt: r.CreatedAt, ReleasedAt: r.ReleasedAt, ReleasedBy: r.ReleasedBy}
		if r.UserID != nil {
			v.Scope, v.UserEmail = "user", emails[*r.UserID]
		}
		out = append(out, v)
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out})
}

func (h *retentionHandlers) createHold(c echo.Context) error {
	var req struct {
		UserID    *int   `json:"user_id"`
		UserEmail string `json:"user_email"` // 二选一：按邮箱指定本组织用户
		Reason    string `json:"reason"`
	}
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "请求格式错误")
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" || len([]rune(req.Reason)) > 500 {
		return echo.NewHTTPError(http.StatusUnprocessableEntity, "必须填写原因（500 字以内）")
	}
	ctx := c.Request().Context()
	p := auth.Current(c)
	if e := strings.TrimSpace(req.UserEmail); e != "" && req.UserID == nil {
		u, err := h.db.User.Query().Where(user.EmailEqualFold(e), user.OrgID(p.OrgID)).Only(ctx)
		if ent.IsNotFound(err) {
			return authz.Deny(c, "legal_hold_user")
		}
		if err != nil {
			return err
		}
		req.UserID = &u.ID
	}
	if req.UserID != nil {
		ok, err := h.db.User.Query().Where(user.ID(*req.UserID), user.OrgID(p.OrgID)).Exist(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return authz.Deny(c, "legal_hold_user")
		}
	}
	scope := "org"
	if req.UserID != nil {
		scope = "user"
	}
	if err := audit.Record(c, audit.Entry{Action: "chat.legal_hold.create", Phase: "attempt", ResourceType: "legal_hold",
		Detail: map[string]any{"scope": scope, "user_id": req.UserID, "reason": req.Reason}}); err != nil {
		return err
	}
	q := h.db.LegalHold.Create().SetOrgID(p.OrgID).SetReason(req.Reason).SetCreatedBy(p.ID)
	if req.UserID != nil {
		q.SetUserID(*req.UserID)
	}
	r, err := q.Save(ctx)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, holdView{ID: r.ID, Scope: scope, UserID: r.UserID, Reason: r.Reason, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt})
}

// releaseHold DELETE /admin/legal-holds/:id：解除（记录保留，写 released_at）。他组织或不存在 404。
func (h *retentionHandlers) releaseHold(c echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return authz.Deny(c, "bad_id")
	}
	ctx := c.Request().Context()
	p := auth.Current(c)
	r, err := h.db.LegalHold.Query().Where(legalhold.ID(id), legalhold.OrgID(p.OrgID)).Only(ctx)
	if ent.IsNotFound(err) {
		return authz.Deny(c, "legal_hold")
	}
	if err != nil {
		return err
	}
	if r.ReleasedAt != nil {
		return echo.NewHTTPError(http.StatusConflict, "该法律保留已解除")
	}
	if err := audit.Record(c, audit.Entry{Action: "chat.legal_hold.release", Phase: "attempt", ResourceType: "legal_hold",
		ResourceID: strconv.Itoa(id), Detail: map[string]any{"user_id": r.UserID}}); err != nil {
		return err
	}
	if err := h.db.LegalHold.UpdateOneID(id).SetReleasedAt(h.clock()).SetReleasedBy(p.ID).Exec(ctx); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}
