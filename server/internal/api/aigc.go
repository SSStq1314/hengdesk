package api

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/aigc"
	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
)

type aigcHandlers struct{ db *ent.Client }

// getAdmin GET /admin/aigc：管理员读取 / 编辑文案。
func (h *aigcHandlers) getAdmin(c echo.Context) error {
	st, err := aigc.Load(c.Request().Context(), h.db)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"settings": st, "defaults": aigc.Defaults()})
}

// putAdmin PUT /admin/aigc：更新文案（不能关闭标识）。
func (h *aigcHandlers) putAdmin(c echo.Context) error {
	var st aigc.Settings
	if err := c.Bind(&st); err != nil {
		return bad("请求格式错误")
	}
	st = st.Normalize()
	if err := st.Validate(); err != nil {
		return bad(err.Error())
	}
	ctx := c.Request().Context()
	old, err := aigc.Load(ctx, h.db)
	if err != nil {
		return err
	}
	p := auth.Current(c)
	if err := audit.Record(c, audit.Entry{Action: "aigc.settings.update", Phase: "attempt", ResourceType: "system_setting",
		ResourceID: aigc.SettingKey, Detail: map[string]any{"old": old, "new": st}}); err != nil {
		return err
	}
	if err := aigc.Save(ctx, h.db, st, p.ID); err != nil {
		return err
	}
	_ = audit.Record(c, audit.Entry{Action: "aigc.settings.update", Phase: "result", Outcome: "ok", ResourceType: "system_setting",
		ResourceID: aigc.SettingKey, Detail: map[string]any{"settings": st}})
	return c.JSON(http.StatusOK, map[string]any{"settings": st})
}

// getPublic GET /aigc：登录用户读取角标与复制后缀（前端展示 / 复制用）。
func (h *aigcHandlers) getPublic(c echo.Context) error {
	st, err := aigc.Load(c.Request().Context(), h.db)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{
		"badge": st.Badge, "suffix": st.Suffix, "producer": st.Producer,
	})
}
