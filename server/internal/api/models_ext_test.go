package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/usagerecord"
)

func TestModelACLDefaultUsage(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	m := g.c.Model.Query().OnlyX(ctx)
	pv := g.c.ModelProvider.Query().OnlyX(ctx)
	mem := g.loginAs(t, "member@x.local", "password123")
	memID := atoi(g.uid(t, "member@x.local"))
	aclURL := fmt.Sprintf("/api/v1/admin/models/%d/acl", m.ID)

	// 成员看不到任何模型，也不能调用默认模型（未设默认 → 404）
	if rec := mem.do(http.MethodGet, "/api/v1/models", "", false); rec.Code != 200 || strings.Contains(rec.Body.String(), "ds-flash") {
		t.Fatalf("my models %d %s", rec.Code, rec.Body)
	}
	noModel := `{"messages":[{"role":"user","content":"hi"}]}`
	if got := g.admin.do(http.MethodPost, "/api/v1/chat/completions", noModel, true).Code; got != 404 {
		t.Fatalf("no default %d", got)
	}
	// 成员无权管理 ACL / 默认 / 用量 / 测试：一律 404
	for _, r := range [][2]string{{http.MethodGet, aclURL}, {http.MethodPost, aclURL}, {http.MethodPost, fmt.Sprintf("/api/v1/admin/models/%d/default", m.ID)},
		{http.MethodGet, "/api/v1/admin/usage/summary"}, {http.MethodPost, fmt.Sprintf("/api/v1/admin/models/%d/test", m.ID)},
		{http.MethodPost, fmt.Sprintf("/api/v1/admin/model-providers/%d/test", pv.ID)}} {
		if got := mem.do(r[0], r[1], `{}`, r[0] != http.MethodGet).Code; got != 404 {
			t.Errorf("%s %s → %d", r[0], r[1], got)
		}
	}
	// 授权给不存在的用户 400；不存在的模型 404
	if got := g.admin.do(http.MethodPost, aclURL, `{"subject_type":"user","subject_id":99999}`, true).Code; got != 400 {
		t.Fatalf("bad subject %d", got)
	}
	if got := g.admin.do(http.MethodGet, "/api/v1/admin/models/99999/acl", "", false).Code; got != 404 {
		t.Fatalf("missing model acl %d", got)
	}
	// 授权 → 列表可见 → 设默认 → 成员不带 model 调用走默认
	if rec := g.admin.do(http.MethodPost, aclURL, fmt.Sprintf(`{"subject_type":"user","subject_id":%d}`, memID), true); rec.Code != 204 {
		t.Fatalf("grant %d %s", rec.Code, rec.Body)
	}
	if rec := g.admin.do(http.MethodGet, aclURL, "", false); !strings.Contains(rec.Body.String(), "member@x.local") {
		t.Fatalf("acl list %s", rec.Body)
	}
	if rec := g.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/models/%d/default", m.ID), "", true); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"is_default":true`) {
		t.Fatalf("default %d %s", rec.Code, rec.Body)
	}
	// 第二个模型设默认后，第一个自动取消
	rec := g.admin.do(http.MethodPost, "/api/v1/admin/models", fmt.Sprintf(`{"name":"ds-pro","provider_id":%d,"upstream_model":"deepseek-v4-pro","price_input_micros":4000000,"price_output_micros":16000000}`, pv.ID), true)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"price_input_micros":4000000`) {
		t.Fatalf("create priced %d %s", rec.Code, rec.Body)
	}
	var m2 struct{ ID int }
	_ = json.Unmarshal(rec.Body.Bytes(), &m2)
	g.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/models/%d/default", m2.ID), "", true)
	g.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/models/%d/default", m.ID), "", true)
	if n := g.c.Model.Query().CountX(ctx); n != 2 {
		t.Fatal(n)
	}
	if g.c.Model.GetX(ctx, m2.ID).IsDefault || !g.c.Model.GetX(ctx, m.ID).IsDefault {
		t.Fatal("最多一个默认模型")
	}
	if rec := mem.do(http.MethodGet, "/api/v1/models", "", false); !strings.Contains(rec.Body.String(), `"name":"ds-flash","is_default":true`) || strings.Contains(rec.Body.String(), "ds-pro") {
		t.Fatalf("my models %s", rec.Body)
	}
	// 设价格后调用，usage 记账（按人、部门、模型）
	g.admin.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/models/%d", m.ID), `{"price_input_micros":1000000,"price_output_micros":2000000}`, true)
	if rec := mem.do(http.MethodPost, "/api/v1/chat/completions", noModel, true); rec.Code != 200 {
		t.Fatalf("default chat %d %s", rec.Code, rec.Body)
	}
	g.m.OmitUsage = true
	if rec := mem.do(http.MethodPost, "/api/v1/chat/completions", `{"stream":true,"messages":[{"role":"user","content":"hi"}]}`, true); rec.Code != 200 {
		t.Fatalf("stream chat %d", rec.Code)
	}
	g.m.OmitUsage = false
	recs := g.c.UsageRecord.Query().Where(usagerecord.UserID(memID)).AllX(ctx)
	if len(recs) != 2 || recs[0].ModelID != m.ID || recs[0].Estimated || !recs[1].Estimated || recs[0].TotalTokens == 0 || recs[0].CostMicros == 0 {
		t.Fatalf("usage records %+v", recs)
	}
	for _, by := range []string{"user", "department", "model", "total"} {
		rec := g.admin.do(http.MethodGet, "/api/v1/admin/usage/summary?group_by="+by, "", false)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"estimated_calls":1`) {
			t.Fatalf("summary %s %d %s", by, rec.Code, rec.Body)
		}
	}
	if rec := g.admin.do(http.MethodGet, "/api/v1/admin/usage/summary?group_by=user", "", false); !strings.Contains(rec.Body.String(), "member@x.local") && !strings.Contains(rec.Body.String(), `"name":"`) {
		t.Fatalf("names %s", rec.Body)
	}
	if got := g.admin.do(http.MethodGet, "/api/v1/admin/usage/summary?group_by=bogus", "", false).Code; got != 400 {
		t.Fatalf("bad group %d", got)
	}
	if rec := mem.do(http.MethodGet, "/api/v1/usage/me", "", false); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"calls":2`) {
		t.Fatalf("me %d %s", rec.Code, rec.Body)
	}
	if rec := g.admin.do(http.MethodGet, "/api/v1/admin/usage/records?from=2000-01-01", "", false); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"estimated":true`) {
		t.Fatalf("records %d %s", rec.Code, rec.Body)
	}
	// 撤销后 404
	if got := g.admin.do(http.MethodDelete, fmt.Sprintf("%s/user/%d", aclURL, memID), "", true).Code; got != 204 {
		t.Fatalf("revoke %d", got)
	}
	if got := mem.do(http.MethodPost, "/api/v1/chat/completions", noModel, true).Code; got != 404 {
		t.Fatalf("after revoke %d", got)
	}
	// 删除模型连带 ACL
	g.admin.do(http.MethodPost, aclURL, fmt.Sprintf(`{"subject_type":"user","subject_id":%d}`, memID), true)
	if got := g.admin.do(http.MethodDelete, fmt.Sprintf("/api/v1/admin/models/%d", m.ID), "", true).Code; got != 204 {
		t.Fatalf("delete %d", got)
	}
	if n := g.c.ACL.Query().CountX(ctx); n != 0 {
		t.Fatalf("acl 残留 %d", n)
	}
	for _, a := range []string{"model.acl.grant", "model.acl.revoke", "model.default.set"} {
		if n := g.c.AuditLog.Query().Where(auditlog.Action(a)).CountX(ctx); n == 0 {
			t.Errorf("缺少审计 %s", a)
		}
	}
}

func TestModelTestConnection(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	m := g.c.Model.Query().OnlyX(ctx)
	pv := g.c.ModelProvider.Query().OnlyX(ctx)
	rec := g.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/model-providers/%d/test", pv.ID), "", true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":true`) || !strings.Contains(rec.Body.String(), "deepseek-flash") {
		t.Fatalf("provider test %d %s", rec.Code, rec.Body)
	}
	rec = g.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/models/%d/test", m.ID), "", true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("model test %d %s", rec.Code, rec.Body)
	}
	if n := g.c.UsageRecord.Query().Where(usagerecord.PurposeEQ(usagerecord.PurposeTest)).CountX(ctx); n != 1 {
		t.Fatalf("test usage %d", n)
	}
	// 错误 Key：ok=false，错误码明确且不泄露 Key
	bad := "sk-wrong-0000000000009876"
	g.admin.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/model-providers/%d", pv.ID), fmt.Sprintf(`{"api_key":%q}`, bad), true)
	rec = g.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/models/%d/test", m.ID), "", true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok":false`) || !strings.Contains(rec.Body.String(), "upstream_auth_failed") || strings.Contains(rec.Body.String(), "9876") {
		t.Fatalf("bad key test %d %s", rec.Code, rec.Body)
	}
	if n := g.c.AuditLog.Query().Where(auditlog.Action("model.test")).CountX(ctx); n != 2 {
		t.Fatalf("model.test 审计 %d", n)
	}
	if got := g.admin.do(http.MethodPost, "/api/v1/admin/models/99999/test", "", true).Code; got != 404 {
		t.Fatalf("missing %d", got)
	}
}
