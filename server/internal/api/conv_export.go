package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/aigc"
	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/message"
)

// export GET /conversations/:id/export?format=md|json：导出会话（含显式后缀与隐式元数据）。
func (h *convHandlers) export(c echo.Context) error {
	p, err := principal(c)
	if err != nil {
		return err
	}
	cv, err := h.own(c, p)
	if err != nil {
		return err
	}
	format := c.QueryParam("format")
	if format == "" {
		format = "md"
	}
	if format != "md" && format != "json" {
		return bad("format 只能是 md / json")
	}
	ctx := c.Request().Context()
	ms, err := h.db.Message.Query().Where(message.ConversationID(cv.ID), message.SupersededAtIsNil()).
		Order(ent.Asc(message.FieldID)).All(ctx)
	if err != nil {
		return err
	}
	st, err := aigc.Load(ctx, h.db)
	if err != nil {
		return err
	}
	st = st.Normalize()
	var lastMeta aigc.Meta
	for _, m := range ms {
		if m.Role == message.RoleAssistant {
			lastMeta = aigc.BuildMeta(st, m.RequestID, fmt.Sprintf("msg-%d", m.ID))
		}
	}
	if lastMeta.ProduceID == "" {
		lastMeta = aigc.BuildMeta(st, "", fmt.Sprintf("conv-%d", cv.ID))
	}
	if err := audit.Record(c, audit.Entry{Action: "conversation.export", Phase: "result", Outcome: "ok",
		ResourceType: "conversation", ResourceID: fmt.Sprintf("%d", cv.ID),
		Detail: map[string]any{"format": format, "messages": len(ms), "produce_id": lastMeta.ProduceID}}); err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "审计不可用，拒绝导出")
	}
	c.Response().Header().Set(aigc.HeaderName, aigc.HeaderValue(lastMeta))
	if format == "json" {
		items := make([]map[string]any, 0, len(ms))
		for _, m := range ms {
			content := m.Content
			if m.Role == message.RoleAssistant {
				content = aigc.AppendSuffix(content, st.Suffix)
			}
			items = append(items, map[string]any{
				"id": m.ID, "role": m.Role, "content": content, "created_at": m.CreatedAt,
				"aigc": m.Role == message.RoleAssistant,
			})
		}
		c.Response().Header().Set(echo.HeaderContentType, "application/json; charset=utf-8")
		c.Response().Header().Set(echo.HeaderContentDisposition, fmt.Sprintf(`attachment; filename="conversation-%d.json"`, cv.ID))
		return c.JSON(http.StatusOK, map[string]any{
			"conversation_id": cv.ID, "title": cv.Title, "exported_at": time.Now().UTC(),
			"aigc": map[string]any{"AIGC": lastMeta}, "messages": items,
		})
	}
	var b strings.Builder
	b.WriteString(aigc.MarkdownComment(lastMeta))
	title := cv.Title
	if title == "" {
		title = "对话"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	for _, m := range ms {
		role := "用户"
		content := m.Content
		if m.Role == message.RoleAssistant {
			role = "助手"
			content = aigc.AppendSuffix(content, st.Suffix)
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", role, content)
	}
	c.Response().Header().Set(echo.HeaderContentType, "text/markdown; charset=utf-8")
	c.Response().Header().Set(echo.HeaderContentDisposition, fmt.Sprintf(`attachment; filename="conversation-%d.md"`, cv.ID))
	return c.String(http.StatusOK, b.String())
}
