package api

// M1 第五块 PR1：远程 MCP 服务器登记与工具发现（FR-22、FR-23 发现部分、FR-27、PRD §9.6）。

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/egress"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/acl"
	"github.com/SSStq1314/hengdesk/server/internal/ent/mcpserver"
	"github.com/SSStq1314/hengdesk/server/internal/ent/tool"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/mcp"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

type mcpHandlers struct {
	db   *ent.Client
	eg   *egress.Client
	keys func() *gateway.Keyring
	cli  *mcp.Client
}

func mountMCP(admin *echo.Group, d Deps, gw *gateway.Service) *mcpHandlers {
	h := &mcpHandlers{db: d.DB, eg: d.Egress, keys: func() *gateway.Keyring { return gw.Keys },
		cli: &mcp.Client{HTTP: d.Egress.HTTP()}}
	need := authz.Require(rbac.ToolManage)
	admin.GET("/mcp-servers", h.list, need)
	admin.POST("/mcp-servers", h.create, need)
	admin.GET("/mcp-servers/:id", h.get, need)
	admin.PATCH("/mcp-servers/:id", h.update, need)
	admin.DELETE("/mcp-servers/:id", h.remove, need)
	admin.POST("/mcp-servers/:id/discover", h.discover, need)
	admin.GET("/mcp-servers/:id/tools", h.tools, need)
	return h
}

type mcpServerView struct {
	ID           int        `json:"id"`
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	URL          string     `json:"url"`
	Transport    string     `json:"transport"`
	HeaderNames  []string   `json:"header_names"`
	TimeoutSec   int        `json:"timeout_sec"`
	Enabled      bool       `json:"enabled"`
	LastStatus   string     `json:"last_status"`
	LastError    string     `json:"last_error"`
	DiscoveredAt *time.Time `json:"discovered_at"`
	ToolCount    int        `json:"tool_count"`
	EnabledTools int        `json:"enabled_tools"`
	ReviewTools  int        `json:"review_tools"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type toolView struct {
	ID                 int            `json:"id"`
	Key                string         `json:"key"`
	Name               string         `json:"name"`
	ServerID           int            `json:"server_id"`
	ServerName         string         `json:"server_name,omitempty"`
	Description        string         `json:"description"`
	InputSchema        map[string]any `json:"input_schema"`
	Annotations        map[string]any `json:"annotations"`
	Enabled            bool           `json:"enabled"`
	ExecMode           string         `json:"exec_mode"`
	RiskLevel          string         `json:"risk_level"`
	SuggestedRisk      string         `json:"suggested_risk"`
	ApproverIDs        []int          `json:"approver_ids"`
	ApprovalTimeoutSec int            `json:"approval_timeout_sec"`
	NeedsReview        bool           `json:"needs_review"`
	Removed            bool           `json:"removed"`
	UpdatedAt          time.Time      `json:"updated_at"`
}

func toToolView(t *ent.Tool) toolView {
	v := toolView{ID: t.ID, Key: t.Key, Name: t.Name, Description: t.Description, InputSchema: t.InputSchema,
		Annotations: t.Annotations, Enabled: t.Enabled, ExecMode: string(t.ExecMode), RiskLevel: string(t.RiskLevel),
		SuggestedRisk: string(t.SuggestedRisk), ApproverIDs: t.ApproverIds, ApprovalTimeoutSec: t.ApprovalTimeoutSec,
		NeedsReview: t.NeedsReview, Removed: t.Removed, UpdatedAt: t.UpdatedAt}
	if t.ServerID != nil {
		v.ServerID = *t.ServerID
	}
	if v.ApproverIDs == nil {
		v.ApproverIDs = []int{}
	}
	return v
}

func (h *mcpHandlers) view(ctx context.Context, s *ent.McpServer) (mcpServerView, error) {
	v := mcpServerView{ID: s.ID, Name: s.Name, Description: s.Description, URL: s.URL, Transport: string(s.Transport),
		HeaderNames: s.HeaderNames, TimeoutSec: s.TimeoutSec, Enabled: s.Enabled, LastStatus: s.LastStatus,
		LastError: s.LastError, DiscoveredAt: s.DiscoveredAt, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
	if v.HeaderNames == nil {
		v.HeaderNames = []string{}
	}
	ts, err := h.db.Tool.Query().Where(tool.ServerID(s.ID), tool.Removed(false)).All(ctx)
	if err != nil {
		return v, err
	}
	for _, t := range ts {
		v.ToolCount++
		if t.Enabled {
			v.EnabledTools++
		}
		if t.NeedsReview {
			v.ReviewTools++
		}
	}
	return v, nil
}

// load 按 ID 取本组织的服务器；其他组织的一律 404。
func (h *mcpHandlers) load(c echo.Context) (*ent.McpServer, error) {
	id, err := idParam(c)
	if err != nil {
		return nil, err
	}
	p := auth.Current(c)
	s, err := h.db.McpServer.Query().Where(mcpserver.ID(id), mcpserver.OrgID(p.OrgID)).Only(c.Request().Context())
	if err != nil {
		return nil, notFoundOr(err)
	}
	return s, nil
}

func (h *mcpHandlers) list(c echo.Context) error {
	ctx := c.Request().Context()
	ss, err := h.db.McpServer.Query().Where(mcpserver.OrgID(auth.Current(c).OrgID)).Order(ent.Asc(mcpserver.FieldID)).All(ctx)
	if err != nil {
		return err
	}
	out := make([]mcpServerView, 0, len(ss))
	for _, s := range ss {
		v, err := h.view(ctx, s)
		if err != nil {
			return err
		}
		out = append(out, v)
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out})
}

func (h *mcpHandlers) get(c echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	v, err := h.view(c.Request().Context(), s)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, v)
}

type mcpServerInput struct {
	Name        *string            `json:"name"`
	Description *string            `json:"description"`
	URL         *string            `json:"url"`
	Transport   *string            `json:"transport"`
	Headers     *map[string]string `json:"headers"` // null / 缺省：不变；{}：清空；其余：整体替换
	TimeoutSec  *int               `json:"timeout_sec"`
	Enabled     *bool              `json:"enabled"`
}

var headerNameRe = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_|~-]{1,64}$`)

// 这些头由传输层自己管理，不允许管理员覆盖。
var reservedHeaders = map[string]bool{"host": true, "content-length": true, "content-type": true, "accept": true,
	"connection": true, "transfer-encoding": true, "mcp-session-id": true, "mcp-protocol-version": true, "last-event-id": true}

func (in *mcpServerInput) validate() error {
	if in.Name != nil {
		n := *in.Name
		if n == "" || len([]rune(n)) > 100 || strings.TrimSpace(n) != n {
			return bad("名称不能为空、不能超过 100 字、首尾不能有空格")
		}
	}
	if in.Description != nil && len([]rune(*in.Description)) > 500 {
		return bad("描述不能超过 500 字")
	}
	if in.URL != nil {
		u, err := url.Parse(*in.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" || len(*in.URL) > 2000 {
			return bad("url 必须是 http(s)://host[:port]/path，且不能包含用户名密码")
		}
	}
	if in.Transport != nil && mcpserver.TransportValidator(mcpserver.Transport(*in.Transport)) != nil {
		return bad("transport 只能是 streamable_http 或 sse（stdio 为 P1，本版本不支持）")
	}
	if in.TimeoutSec != nil && (*in.TimeoutSec < 5 || *in.TimeoutSec > 120) {
		return bad("timeout_sec 范围 5~120")
	}
	if in.Headers != nil {
		if len(*in.Headers) > 20 {
			return bad("鉴权头最多 20 个")
		}
		for k, v := range *in.Headers {
			if !headerNameRe.MatchString(k) || reservedHeaders[strings.ToLower(k)] {
				return bad("鉴权头名称无效或为保留头：" + k)
			}
			if len(v) > 4096 || strings.ContainsAny(v, "\r\n\x00") {
				return bad("鉴权头的值过长或包含非法字符：" + k)
			}
		}
	}
	return nil
}

// checkEgress 端点不在出站白名单时禁止保存（PRD §9.6）。
func (h *mcpHandlers) checkEgress(ctx context.Context, raw string) error {
	if ok, why := h.eg.Check(ctx, raw); !ok {
		host := raw
		if u, err := url.Parse(raw); err == nil {
			host = u.Host
		}
		return echo.NewHTTPError(http.StatusBadRequest, map[string]any{
			"message": "端点 " + host + " 不在出站白名单内（" + egress.ReasonText(why) + "），请先在「出站控制」中添加白名单后再保存",
			"code":    "egress_not_allowed", "reason": why})
	}
	return nil
}

func (h *mcpHandlers) sealHeaders(id int, hd map[string]string) ([]byte, []string, error) {
	if len(hd) == 0 {
		return nil, nil, nil
	}
	b, _ := json.Marshal(hd)
	enc, err := h.keys().SealAAD(gateway.MCPHeadersAAD(id), string(b))
	if errors.Is(err, gateway.ErrNoMasterKey) {
		return nil, nil, echo.NewHTTPError(http.StatusServiceUnavailable, "未配置主密钥，不能保存鉴权头（见 docs/ops/model-gateway.md）")
	}
	if err != nil {
		return nil, nil, err
	}
	names := make([]string, 0, len(hd))
	for k := range hd {
		names = append(names, k)
	}
	sort.Strings(names)
	return enc, names, nil
}

// openHeaders 解密鉴权头；失败时不带任何密文细节。
func (h *mcpHandlers) openHeaders(s *ent.McpServer) (map[string]string, error) {
	if s.HeadersEnc == nil || len(*s.HeadersEnc) == 0 {
		return nil, nil
	}
	pt, err := h.keys().OpenAAD(gateway.MCPHeadersAAD(s.ID), *s.HeadersEnc)
	if err != nil {
		return nil, errors.New("无法解密鉴权头（主密钥不匹配），请重新填写")
	}
	var hd map[string]string
	if err := json.Unmarshal([]byte(pt), &hd); err != nil {
		return nil, errors.New("鉴权头格式损坏，请重新填写")
	}
	return hd, nil
}

func (h *mcpHandlers) create(c echo.Context) error {
	var in mcpServerInput
	if err := c.Bind(&in); err != nil {
		return bad("请求格式错误")
	}
	if in.Name == nil || in.URL == nil {
		return bad("name 和 url 必填")
	}
	if err := in.validate(); err != nil {
		return err
	}
	ctx := c.Request().Context()
	if err := h.checkEgress(ctx, *in.URL); err != nil {
		return err
	}
	p := auth.Current(c)
	tx, err := h.db.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := tx.McpServer.Create().SetOrgID(p.OrgID).SetName(*in.Name).SetURL(*in.URL).SetCreatedBy(p.ID)
	if in.Description != nil {
		q.SetDescription(*in.Description)
	}
	if in.Transport != nil {
		q.SetTransport(mcpserver.Transport(*in.Transport))
	}
	if in.TimeoutSec != nil {
		q.SetTimeoutSec(*in.TimeoutSec)
	}
	if in.Enabled != nil {
		q.SetEnabled(*in.Enabled)
	}
	s, err := q.Save(ctx)
	if err != nil {
		return notFoundOr(err)
	}
	if in.Headers != nil && len(*in.Headers) > 0 {
		enc, names, err := h.sealHeaders(s.ID, *in.Headers)
		if err != nil {
			return err
		}
		if s, err = s.Update().SetHeadersEnc(enc).SetHeaderNames(names).Save(ctx); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := audit.Record(c, audit.Entry{Action: "mcp.server.create", Phase: "result", Outcome: "ok",
		ResourceType: "mcp_server", ResourceID: strconv.Itoa(s.ID), Detail: map[string]any{
			"name": s.Name, "url": s.URL, "transport": string(s.Transport), "header_names": s.HeaderNames}}); err != nil {
		slog.Error("mcp result audit failed", "action", "mcp.server.create", "err", err)
	}
	v, err := h.view(ctx, s)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, v)
}

func (h *mcpHandlers) update(c echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	var in mcpServerInput
	if err := c.Bind(&in); err != nil {
		return bad("请求格式错误")
	}
	if err := in.validate(); err != nil {
		return err
	}
	ctx := c.Request().Context()
	if in.URL != nil && *in.URL != s.URL {
		if err := h.checkEgress(ctx, *in.URL); err != nil {
			return err
		}
	}
	u := s.Update()
	detail := map[string]any{}
	if in.Name != nil {
		u.SetName(*in.Name)
		detail["name"] = *in.Name
	}
	if in.Description != nil {
		u.SetDescription(*in.Description)
	}
	if in.URL != nil {
		u.SetURL(*in.URL)
		detail["url"] = *in.URL
	}
	if in.Transport != nil {
		u.SetTransport(mcpserver.Transport(*in.Transport))
		detail["transport"] = *in.Transport
	}
	if in.TimeoutSec != nil {
		u.SetTimeoutSec(*in.TimeoutSec)
		detail["timeout_sec"] = *in.TimeoutSec
	}
	if in.Enabled != nil {
		u.SetEnabled(*in.Enabled)
		detail["enabled"] = *in.Enabled
	}
	if in.Headers != nil {
		if len(*in.Headers) == 0 {
			u.ClearHeadersEnc().ClearHeaderNames()
			detail["headers"] = "cleared"
		} else {
			enc, names, err := h.sealHeaders(s.ID, *in.Headers)
			if err != nil {
				return err
			}
			u.SetHeadersEnc(enc).SetHeaderNames(names)
			detail["headers"] = "replaced"
			detail["header_names"] = names
		}
	}
	if s, err = u.Save(ctx); err != nil {
		return notFoundOr(err)
	}
	if err := audit.Record(c, audit.Entry{Action: "mcp.server.update", Phase: "result", Outcome: "ok",
		ResourceType: "mcp_server", ResourceID: strconv.Itoa(s.ID), Detail: detail}); err != nil {
		return err
	}
	v, err := h.view(ctx, s)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, v)
}

// remove 删除服务器及其工具与工具授权（审批单保留快照，不受影响）。
func (h *mcpHandlers) remove(c echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	tx, err := h.db.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	ts, err := tx.Tool.Query().Where(tool.ServerID(s.ID)).All(ctx)
	if err != nil {
		return err
	}
	keys := make([]string, len(ts))
	for i, t := range ts {
		keys[i] = t.Key
	}
	if len(keys) > 0 {
		if _, err := tx.ACL.Delete().Where(acl.ResourceType(rbac.ResTool), acl.ResourceIDIn(keys...)).Exec(ctx); err != nil {
			return err
		}
		if _, err := tx.Tool.Delete().Where(tool.ServerID(s.ID)).Exec(ctx); err != nil {
			return err
		}
	}
	if err := tx.McpServer.DeleteOneID(s.ID).Exec(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := audit.Record(c, audit.Entry{Action: "mcp.server.delete", Phase: "result", Outcome: "ok",
		ResourceType: "mcp_server", ResourceID: strconv.Itoa(s.ID), Detail: map[string]any{"name": s.Name, "tools": len(keys)}}); err != nil {
		slog.Error("mcp result audit failed", "action", "mcp.server.delete", "err", err)
	}
	return c.NoContent(http.StatusNoContent)
}

// connParams 组装一次会话的连接参数（解密鉴权头）。
func (h *mcpHandlers) connParams(s *ent.McpServer) (mcp.Server, error) {
	hd, err := h.openHeaders(s)
	if err != nil {
		return mcp.Server{}, err
	}
	return mcp.Server{URL: s.URL, Transport: string(s.Transport), Headers: hd, Timeout: time.Duration(s.TimeoutSec) * time.Second}, nil
}

// mcpErrorText 把连接 / 协议错误转成给管理员看的中文提示（不含鉴权头）。
func mcpErrorText(err error, secrets ...string) string {
	var de *egress.DeniedError
	if errors.As(err, &de) {
		return de.Message()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "连接 MCP 服务器超时"
	}
	msg := gateway.Redact(err.Error(), secrets...)
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return "连接 MCP 服务器失败：" + msg
}

func headerValues(hd map[string]string) []string {
	out := make([]string, 0, len(hd))
	for _, v := range hd {
		out = append(out, v)
	}
	return out
}

// discover 连接服务器拉取工具列表并同步到工具目录。
func (h *mcpHandlers) discover(c echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	if !s.Enabled {
		return echo.NewHTTPError(http.StatusConflict, "服务器已停用，请先启用")
	}
	ctx := c.Request().Context()
	fail := func(status int, msg string) error {
		_ = h.db.McpServer.UpdateOneID(s.ID).SetLastStatus("failed").SetLastError(msg).Exec(context.WithoutCancel(ctx))
		if err := audit.Record(c, audit.Entry{Action: "mcp.server.discover", Phase: "result", Outcome: "error",
			ResourceType: "mcp_server", ResourceID: strconv.Itoa(s.ID), Detail: map[string]any{"name": s.Name, "error": msg}}); err != nil {
			return err
		}
		return echo.NewHTTPError(status, msg)
	}
	sv, err := h.connParams(s)
	if err != nil {
		return fail(http.StatusConflict, err.Error())
	}
	found, err := h.cli.ListTools(egressCtx(ctx, c, "mcp"), sv)
	if err != nil {
		return fail(http.StatusBadGateway, mcpErrorText(err, headerValues(sv.Headers)...))
	}
	tx, err := h.db.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	st, err := mcp.Sync(ctx, tx, s.OrgID, s.ID, found)
	if err != nil {
		return err
	}
	if err := tx.McpServer.UpdateOneID(s.ID).SetLastStatus("ok").SetLastError("").SetDiscoveredAt(time.Now()).Exec(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := audit.Record(c, audit.Entry{Action: "mcp.server.discover", Phase: "result", Outcome: "ok",
		ResourceType: "mcp_server", ResourceID: strconv.Itoa(s.ID), Detail: map[string]any{"name": s.Name,
			"added": st.Added, "changed": st.Changed, "removed": st.Removed, "restored": st.Restored, "unchanged": st.Unchanged}}); err != nil {
		slog.Error("mcp result audit failed", "action", "mcp.server.discover", "err", err)
	}
	ts, err := h.listTools(ctx, s.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"stats": st, "tools": ts})
}

func (h *mcpHandlers) listTools(ctx context.Context, serverID int) ([]toolView, error) {
	ts, err := h.db.Tool.Query().Where(tool.ServerID(serverID)).Order(ent.Asc(tool.FieldName)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]toolView, len(ts))
	for i, t := range ts {
		out[i] = toToolView(t)
	}
	return out, nil
}

func (h *mcpHandlers) tools(c echo.Context) error {
	s, err := h.load(c)
	if err != nil {
		return err
	}
	ts, err := h.listTools(c.Request().Context(), s.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"items": ts})
}
