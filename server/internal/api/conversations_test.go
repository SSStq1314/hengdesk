package api_test

// M1 第三块 PR1：会话与消息。只有本人可操作（其他人一律 404）、SSE 流式、停止后落库、模型权限、审计 fail-closed。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/message"
	"github.com/SSStq1314/hengdesk/server/internal/ent/model"
	"github.com/SSStq1314/hengdesk/server/internal/ent/usagerecord"
)

func newConv(t *testing.T, cl *client, body string) int {
	t.Helper()
	rec := cl.do(http.MethodPost, "/api/v1/conversations", body, true)
	if rec.Code != 201 {
		t.Fatalf("create conversation %d %s", rec.Code, rec.Body)
	}
	var v struct{ ID int }
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	return v.ID
}

func TestConversationOwnerOnly(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	primary := g.c.Model.Query().Where(model.Name("ds-flash")).OnlyIDX(ctx)
	grantUse(t, g, primary, atoi(g.uid(t, "member@x.local")))
	mem := g.loginAs(t, "member@x.local", "password123")
	id := newConv(t, mem, `{"title":"我的会话"}`)
	if rec := mem.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", id), `{"content":"你好","model":"ds-flash"}`, true); rec.Code != 200 {
		t.Fatalf("send %d %s", rec.Code, rec.Body)
	}
	// 超级管理员也不能看别人的会话：一律 404，与不存在的会话一致
	for _, r := range []struct{ m, p, b string }{
		{http.MethodGet, "/api/v1/conversations/%d", ""},
		{http.MethodPatch, "/api/v1/conversations/%d", `{"title":"x"}`},
		{http.MethodDelete, "/api/v1/conversations/%d", ""},
		{http.MethodPost, "/api/v1/conversations/%d/messages", `{"content":"hi","model":"ds-flash"}`},
	} {
		if rec := g.admin.do(r.m, fmt.Sprintf(r.p, id), r.b, true); rec.Code != 404 {
			t.Fatalf("%s 非本人应 404：%d %s", r.m, rec.Code, rec.Body)
		}
		if rec := mem.do(r.m, fmt.Sprintf(r.p, 999999), r.b, true); rec.Code != 404 {
			t.Fatalf("%s 不存在应 404：%d", r.m, rec.Code)
		}
	}
	if rec := g.admin.do(http.MethodGet, "/api/v1/conversations", "", false); strings.Contains(rec.Body.String(), "我的会话") {
		t.Fatalf("列表不应包含他人会话 %s", rec.Body)
	}
	if n := g.c.AuditLog.Query().Where(auditlog.Action("authz.denied")).CountX(ctx); n < 4 {
		t.Fatalf("越权应记审计 %d", n)
	}
	rec := mem.do(http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d", id), "", false)
	var got struct {
		Messages []struct{ Role, Content, Status, ModelName string } `json:"messages"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Messages) != 2 || got.Messages[1].Content != "你好，我是模拟模型。" || got.Messages[1].Status != "done" {
		t.Fatalf("messages %s", rec.Body)
	}
	// 本人改名、删除；删除后 404
	if rec := mem.do(http.MethodPatch, fmt.Sprintf("/api/v1/conversations/%d", id), `{"title":"新标题"}`, true); rec.Code != 200 || !strings.Contains(rec.Body.String(), "新标题") {
		t.Fatalf("rename %d %s", rec.Code, rec.Body)
	}
	if rec := mem.do(http.MethodDelete, fmt.Sprintf("/api/v1/conversations/%d", id), "", true); rec.Code != 204 {
		t.Fatalf("delete %d", rec.Code)
	}
	if rec := mem.do(http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d", id), "", false); rec.Code != 404 {
		t.Fatalf("删除后应 404：%d", rec.Code)
	}
	if n := g.c.Message.Query().CountX(ctx); n != 2 {
		t.Fatalf("软删除应保留消息供审计 %d", n)
	}
}

func TestConversationStreamAndHistory(t *testing.T) {
	g := newGW(t, false)
	id := newConv(t, g.admin, `{}`)
	rec := g.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", id), `{"content":"第一个问题","model":"ds-flash"}`, true)
	b := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(b, "event: token") || !strings.Contains(b, "event: done") || !strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("应为 SSE：%d %s", rec.Code, b)
	}
	rec = g.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", id), `{"content":"第二个问题"}`, true)
	if rec.Code != 200 {
		t.Fatalf("second %d %s", rec.Code, rec.Body)
	}
	last := g.m.Requests[len(g.m.Requests)-1]
	msgs, _ := last["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("应带上历史消息（用户、助手、本条）：%v", msgs)
	}
	// 标题取首条消息
	if rec := g.admin.do(http.MethodGet, "/api/v1/conversations", "", false); !strings.Contains(rec.Body.String(), "第一个问题") {
		t.Fatalf("title %s", rec.Body)
	}
	if n := g.c.UsageRecord.Query().Where(usagerecord.Stream(true)).CountX(context.Background()); n != 2 {
		t.Fatalf("应记用量 %d", n)
	}
	if n := g.c.AuditLog.Query().Where(auditlog.Action("chat.message.send")).CountX(context.Background()); n != 4 {
		t.Fatalf("应有 attempt + result 审计 %d", n)
	}
}

func TestConversationModelPermission(t *testing.T) {
	g := newGW(t, false)
	mem := g.loginAs(t, "member@x.local", "password123")
	id := newConv(t, mem, `{}`)
	rec := mem.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", id), `{"content":"hi","model":"ds-flash"}`, true)
	if rec.Code != 404 || g.m.Count() != 0 {
		t.Fatalf("无模型权限应 404 且不调用上游：%d %d", rec.Code, g.m.Count())
	}
	if rec := mem.do(http.MethodPost, "/api/v1/conversations", `{"model":"ds-flash"}`, true); rec.Code != 404 {
		t.Fatalf("创建时选无权限模型应 404：%d", rec.Code)
	}
	if n := g.c.Message.Query().CountX(context.Background()); n != 0 {
		t.Fatalf("被拒绝时不应写消息 %d", n)
	}
}

func TestConversationStopSavesPartial(t *testing.T) {
	fastRetry(t)
	g := newGW(t, false)
	id := newConv(t, g.admin, `{}`)
	g.m.StallAfter = 2
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", id), strings.NewReader(`{"content":"写一首诗","model":"ds-flash"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.CSRFHeader, g.admin.csrf)
	for _, ck := range g.admin.cks {
		req.AddCookie(ck)
	}
	w := httptest.NewRecorder()
	// mock 上游输出「你好」两个字后挂起；随后取消请求，模拟用户点「停止」（前端中断 fetch）
	stall := make(chan struct{})
	go func() {
		for g.m.Count() == 0 {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
		close(stall)
		cancel()
	}()
	g.h.ServeHTTP(w, req)
	<-stall
	m := g.c.Message.Query().Where(message.RoleEQ(message.RoleAssistant)).OnlyX(context.Background())
	if m.Status != message.StatusStopped || m.Content != "你好" {
		t.Fatalf("停止后应保存已生成部分：status=%s content=%q", m.Status, m.Content)
	}
	if g.m.Count() != 1 {
		t.Fatalf("停止后不应重试 %d", g.m.Count())
	}
	if n := g.c.UsageRecord.Query().CountX(context.Background()); n != 1 {
		t.Fatalf("停止的生成照常计量 %d", n)
	}
}

func TestConversationAuditFailClosed(t *testing.T) {
	g := newGW(t, true) // model.call 审计写失败
	id := newConv(t, g.admin, `{}`)
	rec := g.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", id), `{"content":"hi","model":"ds-flash"}`, true)
	if rec.Code < 500 || g.m.Count() != 0 {
		t.Fatalf("审计写失败应拒绝且不调用上游：%d %d %s", rec.Code, g.m.Count(), rec.Body)
	}
	m := g.c.Message.Query().Where(message.RoleEQ(message.RoleAssistant)).OnlyX(context.Background())
	if m.Status != message.StatusError || m.ErrorCode != "audit_failed" {
		t.Fatalf("assistant 消息应标记失败 %+v", m)
	}
}
