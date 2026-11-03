package api

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
)

type auditHandlers struct{ db *ent.Client }

// query 按条件过滤：actor（邮箱包含）、action（前缀）、outcome、from/to（RFC3339）、before_id（游标）。
// 范围为本组织记录以及无组织归属的记录（如未知账号的登录失败）。
func (h *auditHandlers) query(c echo.Context) (*ent.AuditLogQuery, error) {
	p := auth.Current(c)
	q := h.db.AuditLog.Query().Where(auditlog.Or(auditlog.OrgID(p.OrgID), auditlog.OrgIDIsNil()))
	if v := c.QueryParam("actor"); v != "" {
		q.Where(auditlog.ActorEmailContainsFold(v))
	}
	if v := c.QueryParam("action"); v != "" {
		q.Where(auditlog.ActionHasPrefix(v))
	}
	if v := c.QueryParam("outcome"); v != "" {
		q.Where(auditlog.Outcome(v))
	}
	for k, f := range map[string]func(time.Time){
		"from": func(t time.Time) { q.Where(auditlog.TsGTE(t)) },
		"to":   func(t time.Time) { q.Where(auditlog.TsLT(t)) },
	} {
		if v := c.QueryParam(k); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return nil, echo.NewHTTPError(http.StatusBadRequest, k+" 须为 RFC3339 时间")
			}
			f(t)
		}
	}
	return q, nil
}

func (h *auditHandlers) list(c echo.Context) error {
	q, err := h.query(c)
	if err != nil {
		return err
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if b, _ := strconv.Atoi(c.QueryParam("before_id")); b > 0 {
		q.Where(auditlog.IDLT(b))
	}
	rows, err := q.Order(ent.Desc(auditlog.FieldID)).Limit(limit + 1).All(c.Request().Context())
	if err != nil {
		return err
	}
	next := 0
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[len(rows)-1].ID
	}
	return c.JSON(http.StatusOK, map[string]any{"items": rows, "next_before_id": next})
}

const exportMax = 10000

func (h *auditHandlers) export(c echo.Context) error {
	q, err := h.query(c)
	if err != nil {
		return err
	}
	rows, err := q.Order(ent.Desc(auditlog.FieldID)).Limit(exportMax).All(c.Request().Context())
	if err != nil {
		return err
	}
	c.Response().Header().Set(echo.HeaderContentType, "text/csv; charset=utf-8")
	c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="audit-logs.csv"`)
	c.Response().WriteHeader(http.StatusOK)
	_, _ = c.Response().Write([]byte("\xEF\xBB\xBF")) // BOM，Excel 正确识别 UTF-8
	w := csv.NewWriter(c.Response())
	_ = w.Write([]string{"id", "ts", "actor_email", "action", "phase", "outcome", "resource_type", "resource_id", "ip", "request_id", "hash"})
	for _, r := range rows {
		_ = w.Write([]string{strconv.Itoa(r.ID), r.Ts.Format(time.RFC3339), csvSafe(r.ActorEmail), csvSafe(r.Action), string(r.Phase),
			csvSafe(r.Outcome), csvSafe(r.ResourceType), csvSafe(r.ResourceID), r.IP, r.RequestID, r.Hash})
	}
	w.Flush()
	return w.Error()
}

// csvSafe 防 CSV 公式注入。
func csvSafe(s string) string {
	if s != "" && (s[0] == '=' || s[0] == '+' || s[0] == '-' || s[0] == '@' || s[0] == '\t' || s[0] == '\r') {
		return "'" + s
	}
	return s
}

func (h *auditHandlers) verify(c echo.Context) error {
	res, err := audit.Verify(c.Request().Context(), h.db)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, res)
}
