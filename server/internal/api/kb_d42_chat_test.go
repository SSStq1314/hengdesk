package api_test

// D-42 对话侧：检索失败 503 + 「本次不使用知识库继续」；无命中标注「未引用知识库资料」；「仅依据资料回答」无资料不调用模型。

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/ent/knowledgebase"
	"github.com/SSStq1314/hengdesk/server/internal/ent/message"
	"github.com/SSStq1314/hengdesk/server/internal/ent/model"
)

func TestChatKBD42Unavailable(t *testing.T) {
	e := newKBEnv(t)
	chatReady(t, e)
	if _, r := e.up(t, e.owner, "差旅.md", policyDoc); r.File.Status != "ready" {
		t.Fatal("index")
	}
	id := newConv(t, e.owner, fmt.Sprintf(`{"kb_ids":[%d]}`, e.kbID))
	// embedding 模型停用 → 检索失败 → 503 + 结构化错误码，不调用模型
	e.c.Model.Update().Where(model.Name("emb")).SetEnabled(false).ExecX(t.Context())
	calls := e.m.Count()
	rec := e.owner.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", id), `{"content":"住宿标准？","model":"ds-flash"}`, true)
	if rec.Code != 503 || e.m.Count() != calls || !strings.Contains(rec.Body.String(), `"code":"kb_failed"`) || !strings.Contains(rec.Body.String(), "知识库暂时不可用") {
		t.Fatalf("检索失败应 503 kb_failed 且不调用模型：%d %s", rec.Code, rec.Body)
	}
	last := e.c.Message.Query().Where(message.ConversationID(id)).Order(message.ByID()).AllX(t.Context())
	failed := last[len(last)-1]
	if failed.Status != message.StatusError || failed.ErrorCode != "kb_failed" {
		t.Fatalf("失败消息：%+v", failed)
	}
	// 本次不使用知识库继续 → 调用模型，回答标注 kb_ungrounded
	rec = e.owner.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/regenerate", id), fmt.Sprintf(`{"message_id":%d,"skip_kb":true}`, failed.ID), true)
	if rec.Code != 200 || e.m.Count() != calls+1 || !strings.Contains(rec.Body.String(), `"kb_ungrounded":true`) || strings.Contains(rec.Body.String(), "event: citation") {
		t.Fatalf("跳过知识库应正常回答并标注：%d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(auditDetail(t, e, "chat.message.regenerate"), `"kb_skipped":true`) {
		t.Fatal("审计应记录 kb_skipped")
	}
	rec = e.owner.do(http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d", id), "", false)
	if !strings.Contains(rec.Body.String(), `"kb_ungrounded":true`) {
		t.Fatalf("kb_ungrounded 应落库：%s", rec.Body)
	}
	// 开启「仅依据资料回答」后不能跳过
	if rec := e.owner.do(http.MethodPatch, e.path(""), `{"strict_grounding":true}`, true); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"strict_grounding":true`) {
		t.Fatalf("开启 strict：%d %s", rec.Code, rec.Body)
	}
	lastID := e.c.Message.Query().Where(message.ConversationID(id)).Order(message.ByID()).AllX(t.Context())
	rec = e.owner.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/regenerate", id), fmt.Sprintf(`{"message_id":%d,"skip_kb":true}`, lastID[len(lastID)-1].ID), true)
	if rec.Code != 409 || e.m.Count() != calls+1 {
		t.Fatalf("strict 库不能跳过：%d %s", rec.Code, rec.Body)
	}
}

func TestChatKBD42NoHits(t *testing.T) {
	e := newKBEnv(t)
	chatReady(t, e)
	id := newConv(t, e.owner, fmt.Sprintf(`{"kb_ids":[%d]}`, e.kbID))
	send := fmt.Sprintf("/api/v1/conversations/%d/messages", id)
	// 默认：无资料 → 通用知识回答 + 标注
	calls := e.m.Count()
	rec := e.owner.do(http.MethodPost, send, `{"content":"住宿标准？","model":"ds-flash"}`, true)
	if rec.Code != 200 || e.m.Count() != calls+1 || !strings.Contains(rec.Body.String(), `"kb_ungrounded":true`) {
		t.Fatalf("无资料应回答并标注：%d %s", rec.Code, rec.Body)
	}
	// 仅依据资料回答：无资料 → 不调用模型，固定回复
	e.c.KnowledgeBase.UpdateOneID(e.kbID).SetStrictGrounding(true).ExecX(t.Context())
	rec = e.owner.do(http.MethodPost, send, `{"content":"住宿标准？","model":"ds-flash"}`, true)
	if rec.Code != 200 || e.m.Count() != calls+1 || !strings.Contains(rec.Body.String(), "仅依据资料回答") || !strings.Contains(rec.Body.String(), "event: done") {
		t.Fatalf("strict 无资料不应调用模型：%d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(auditDetail(t, e, "chat.message.send"), `"kb_strict_refused":true`) {
		t.Fatal("审计应记录 kb_strict_refused")
	}
	// 有资料时 strict 库正常回答
	if _, r := e.up(t, e.owner, "差旅.md", policyDoc); r.File.Status != "ready" {
		t.Fatal("index")
	}
	rec = e.owner.do(http.MethodPost, send, `{"content":"出差住宿标准是多少？","model":"ds-flash"}`, true)
	if rec.Code != 200 || e.m.Count() != calls+2 || !strings.Contains(rec.Body.String(), "event: citation") || strings.Contains(rec.Body.String(), `"kb_ungrounded":true`) {
		t.Fatalf("strict 有资料应正常回答：%d %s", rec.Code, rec.Body)
	}
	_ = knowledgebase.FieldStrictGrounding
}
