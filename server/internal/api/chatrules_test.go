package api_test

// M1 第三块 PR2b（D-40）：会话生成锁（409）、重新生成 / 重试最后一条回复、记住的模型失效回退默认模型。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/message"
	"github.com/SSStq1314/hengdesk/server/internal/ent/model"
)

type convMsg struct {
	ID      int
	Role    string
	Content string
	Status  string
}

func convMessages(t *testing.T, cl *client, id int) []convMsg {
	t.Helper()
	rec := cl.do(http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d", id), "", false)
	var v struct{ Messages []convMsg }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &v) != nil {
		t.Fatalf("get %d %s", rec.Code, rec.Body)
	}
	return v.Messages
}

func TestConversationRegenerate(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	id := newConv(t, g.admin, `{}`)
	send := fmt.Sprintf("/api/v1/conversations/%d/messages", id)
	regen := fmt.Sprintf("/api/v1/conversations/%d/regenerate", id)
	for _, q := range []string{"问题一", "问题二"} {
		if rec := g.admin.do(http.MethodPost, send, fmt.Sprintf(`{"content":%q,"model":"ds-flash"}`, q), true); rec.Code != 200 {
			t.Fatalf("send %d %s", rec.Code, rec.Body)
		}
	}
	ms := convMessages(t, g.admin, id)
	first, last := ms[1].ID, ms[3].ID
	// 只能重新生成最后一条回复；用户消息、更早的回复都不行
	for _, mid := range []int{first, ms[2].ID} {
		if rec := g.admin.do(http.MethodPost, regen, fmt.Sprintf(`{"message_id":%d}`, mid), true); rec.Code != 409 {
			t.Fatalf("非最后一条应 409：%d %s", rec.Code, rec.Body)
		}
	}
	// 非本人 / 不存在的消息一律 404
	mem := g.loginAs(t, "member@x.local", "password123")
	if rec := mem.do(http.MethodPost, regen, fmt.Sprintf(`{"message_id":%d}`, last), true); rec.Code != 404 {
		t.Fatalf("非本人应 404：%d", rec.Code)
	}
	if rec := g.admin.do(http.MethodPost, regen, `{"message_id":999999}`, true); rec.Code != 404 {
		t.Fatalf("不存在的消息应 404：%d", rec.Code)
	}
	calls := g.m.Count()
	rec := g.admin.do(http.MethodPost, regen, fmt.Sprintf(`{"message_id":%d}`, last), true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "event: done") || g.m.Count() != calls+1 {
		t.Fatalf("regenerate %d %s", rec.Code, rec.Body)
	}
	// 上下文：问题一、回答一、问题二（不含被替换的旧回复）
	req := g.m.Requests[len(g.m.Requests)-1]
	if msgs, _ := req["messages"].([]any); len(msgs) != 3 {
		t.Fatalf("重新生成的上下文应为 3 条：%v", msgs)
	}
	ms = convMessages(t, g.admin, id)
	if len(ms) != 4 || ms[3].ID == last || ms[3].Status != "done" {
		t.Fatalf("UI 只显示最新回复 %+v", ms)
	}
	old := g.c.Message.GetX(ctx, last)
	if old.SupersededAt == nil || old.Content == "" {
		t.Fatalf("旧回复应软保留 %+v", old)
	}
	// 旧 id 不能再重新生成
	if rec := g.admin.do(http.MethodPost, regen, fmt.Sprintf(`{"message_id":%d}`, last), true); rec.Code != 409 {
		t.Fatalf("已替换的回复应 409：%d", rec.Code)
	}
	// 下一轮上下文只含最新回复：问题一、回答一、问题二、新回答二、问题三
	if rec := g.admin.do(http.MethodPost, send, `{"content":"问题三"}`, true); rec.Code != 200 {
		t.Fatalf("send %d", rec.Code)
	}
	req = g.m.Requests[len(g.m.Requests)-1]
	if msgs, _ := req["messages"].([]any); len(msgs) != 5 {
		t.Fatalf("下一轮上下文应为 5 条：%v", msgs)
	}
	if n := g.c.AuditLog.Query().Where(auditlog.Action("chat.message.regenerate"), auditlog.PhaseEQ(auditlog.PhaseAttempt)).CountX(ctx); n != 1 {
		t.Fatalf("重新生成应记审计 %d", n)
	}

	// 失败消息「重试」走同一接口，审计 kind=retry
	ms = convMessages(t, g.admin, id)
	g.c.Message.UpdateOneID(ms[len(ms)-1].ID).SetStatus(message.StatusError).SetErrorCode("upstream_error").ExecX(ctx)
	if rec := g.admin.do(http.MethodPost, regen, fmt.Sprintf(`{"message_id":%d}`, ms[len(ms)-1].ID), true); rec.Code != 200 {
		t.Fatalf("retry %d %s", rec.Code, rec.Body)
	}
	a := g.c.AuditLog.Query().Where(auditlog.Action("chat.message.regenerate"), auditlog.PhaseEQ(auditlog.PhaseAttempt)).
		Order(auditlog.ByID()).AllX(ctx)
	if len(a) != 2 || a[1].Detail["kind"] != "retry" {
		t.Fatalf("重试审计 %+v", a)
	}
}

func TestConversationLockUnavailable(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	id := newConv(t, g.admin, `{}`)
	if rec := g.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", id), `{"content":"hi","model":"ds-flash"}`, true); rec.Code != 200 {
		t.Fatalf("send %d", rec.Code)
	}
	last := g.c.Message.Query().Where(message.RoleEQ(message.RoleAssistant)).OnlyX(ctx)
	g.mr.Close() // 会话锁不可用 → 503，不生成
	rec := g.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/regenerate", id), fmt.Sprintf(`{"message_id":%d}`, last.ID), true)
	if rec.Code != 503 || g.m.Count() != 1 {
		t.Fatalf("Valkey 不可用应 503 且不调用上游：%d %s", rec.Code, rec.Body)
	}
}

func TestConversationGenerationLock(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	id := newConv(t, g.admin, `{}`)
	send := fmt.Sprintf("/api/v1/conversations/%d/messages", id)
	if rec := g.admin.do(http.MethodPost, send, `{"content":"hi","model":"ds-flash"}`, true); rec.Code != 200 {
		t.Fatalf("send %d", rec.Code)
	}
	key := fmt.Sprintf("hengdesk:chat:gen:%d", id)
	if g.mr.Exists(key) {
		t.Fatal("生成结束后应释放锁")
	}
	// 模拟另一条正在生成
	if err := g.mr.Set(key, "other"); err != nil {
		t.Fatal(err)
	}
	g.mr.SetTTL(key, 60e9)
	calls, msgs := g.m.Count(), g.c.Message.Query().CountX(ctx)
	if rec := g.admin.do(http.MethodPost, send, `{"content":"再来"}`, true); rec.Code != 409 {
		t.Fatalf("生成中应 409：%d %s", rec.Code, rec.Body)
	}
	last := g.c.Message.Query().Where(message.RoleEQ(message.RoleAssistant)).OnlyX(ctx)
	if rec := g.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/regenerate", id), fmt.Sprintf(`{"message_id":%d}`, last.ID), true); rec.Code != 409 {
		t.Fatalf("生成中重新生成应 409：%d", rec.Code)
	}
	if g.m.Count() != calls || g.c.Message.Query().CountX(ctx) != msgs {
		t.Fatal("409 时不应调用上游或写消息")
	}
	if v, _ := g.mr.Get(key); v != "other" {
		t.Fatal("不应释放别人的锁")
	}
	// 锁自动过期后恢复
	g.mr.FastForward(61e9)
	if rec := g.admin.do(http.MethodPost, send, `{"content":"再来"}`, true); rec.Code != 200 {
		t.Fatalf("锁过期后应可发送：%d %s", rec.Code, rec.Body)
	}
}

func TestConversationRememberedModelFallback(t *testing.T) {
	g := newGW(t, false)
	ctx := context.Background()
	backup, _, bid := addBackupModel(t, g)
	g.c.Model.Update().Where(model.Name("ds-flash")).SetIsDefault(true).ExecX(ctx)
	id := newConv(t, g.admin, `{}`)
	send := fmt.Sprintf("/api/v1/conversations/%d/messages", id)
	if rec := g.admin.do(http.MethodPost, send, `{"content":"hi","model":"ds-backup"}`, true); rec.Code != 200 || backup.Count() != 1 {
		t.Fatalf("send %d %s", rec.Code, rec.Body)
	}
	// 记住的模型被停用 → 回退默认模型并提示
	g.c.Model.UpdateOneID(bid).SetEnabled(false).ExecX(ctx)
	rec := g.admin.do(http.MethodPost, send, `{"content":"继续"}`, true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `已改用默认模型「ds-flash」`) || backup.Count() != 1 {
		t.Fatalf("应回退默认模型并提示：%d %s", rec.Code, rec.Body)
	}
	cv := g.c.Conversation.GetX(ctx, id)
	if cv.ModelID == nil || *cv.ModelID == bid {
		t.Fatal("会话记住的模型应更新为默认模型")
	}
	// 默认模型也不可用 → 明确错误，不写消息
	g.c.Conversation.UpdateOneID(id).SetModelID(bid).ExecX(ctx)
	g.c.Model.Update().Where(model.Name("ds-flash")).SetEnabled(false).ExecX(ctx)
	msgs := g.c.Message.Query().CountX(ctx)
	rec = g.admin.do(http.MethodPost, send, `{"content":"还在吗"}`, true)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "没有可用的默认模型") || g.c.Message.Query().CountX(ctx) != msgs {
		t.Fatalf("默认模型不可用应明确报错：%d %s", rec.Code, rec.Body)
	}
}
