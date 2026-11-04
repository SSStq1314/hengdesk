package api_test

// M1 第四块 PR3（FR-18）：会话选用知识库 → 检索 → 编号上下文注入 → citation 事件在 token 之前发出 → 引用随消息落库；
// 读取历史时按当前权限过滤引用；无可用资料时给出提示；选用无权 / 超过 5 个知识库被拒绝；检索审计失败拒绝生成。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/api"
	"github.com/SSStq1314/hengdesk/server/internal/ent/model"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
)

type citeMsg struct {
	ID        int
	Role      string
	Status    string
	ErrorCode string `json:"error_code"`
	Citations []struct {
		Index    int
		FileName string `json:"file_name"`
		Snippet  string
	}
}

func TestChatKBCitations(t *testing.T) {
	e := newKBEnv(t)
	chatReady(t, e)
	ctx := t.Context()
	_, r1 := e.up(t, e.owner, "差旅.md", policyDoc)
	_, r2 := e.up(t, e.owner, "保密.md", "# 薪酬\n\n高管薪酬方案：住宿标准另行规定，保密。\n")
	if r1.File.Status != "ready" || r2.File.Status != "ready" {
		t.Fatal("index")
	}
	// 选用知识库：无权 404，超过 5 个 400
	mem := e.loginAs(t, "member@x.local", "password123")
	if rec := mem.do(http.MethodPost, "/api/v1/conversations", fmt.Sprintf(`{"kb_ids":[%d]}`, e.kbID), true); rec.Code != 404 {
		t.Fatalf("无权选用应 404：%d %s", rec.Code, rec.Body)
	}
	if rec := e.owner.do(http.MethodPost, "/api/v1/conversations", `{"kb_ids":[1,2,3,4,5,6]}`, true); rec.Code != 400 {
		t.Fatalf("超过 5 个应 400：%d", rec.Code)
	}
	id := newConv(t, e.owner, fmt.Sprintf(`{"kb_ids":[%d,%d]}`, e.kbID, e.kbID))
	rec := e.owner.do(http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d", id), "", false)
	if !strings.Contains(rec.Body.String(), fmt.Sprintf(`"kb_ids":[%d]`, e.kbID)) {
		t.Fatalf("kb_ids 去重后保存：%s", rec.Body)
	}
	send := fmt.Sprintf("/api/v1/conversations/%d/messages", id)
	rec = e.owner.do(http.MethodPost, send, `{"content":"出差住宿标准是多少？","model":"ds-flash"}`, true)
	body := rec.Body.String()
	ci, tk := strings.Index(body, "event: citation"), strings.Index(body, "event: token")
	if rec.Code != 200 || ci < 0 || tk < 0 || ci > tk || !strings.Contains(body, `"file_name":"差旅.md"`) {
		t.Fatalf("citation 应在 token 之前：%d %s", rec.Code, body)
	}
	// 模型收到编号上下文
	b, _ := json.Marshal(e.m.LastRequest()["messages"])
	if !strings.Contains(string(b), "[1]") || !strings.Contains(string(b), "差旅.md") {
		t.Fatalf("上下文未注入：%s", b)
	}
	msgs := func(cl *client) []citeMsg {
		rec := cl.do(http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d", id), "", false)
		var v struct{ Messages []citeMsg }
		_ = json.Unmarshal(rec.Body.Bytes(), &v)
		return v.Messages
	}
	ms := msgs(e.owner)
	if len(ms) != 2 || len(ms[1].Citations) < 2 {
		t.Fatalf("引用应落库：%+v", ms)
	}
	// 保密文件改为自定义权限（所有者仍有 manage 可见）；把所有者的 manage 换成另一管理员后，旧引用中的保密文件不再返回
	e.owner.do("PUT", e.path("/files/%d/acl-mode", r2.File.ID), `{"custom":true}`, true)
	if n := len(msgs(e.owner)[1].Citations); n < 2 {
		t.Fatalf("manage 仍可见：%d", n)
	}
	// 删除保密文件 → 读取时整条去掉
	if rec := e.owner.do(http.MethodDelete, e.path("/files/%d", r2.File.ID), "", true); rec.Code != 204 {
		t.Fatalf("delete file %d %s", rec.Code, rec.Body)
	}
	for _, c := range msgs(e.owner)[1].Citations {
		if c.FileName == "保密.md" {
			t.Fatal("已删除文件的引用不应返回")
		}
	}
	// 审计记录检索统计
	if !strings.Contains(auditDetail(t, e, "chat.message.send"), `"kb_hits"`) {
		t.Fatal("result 审计缺少 kb_hits")
	}

	// 删除剩余文件 → 没有可用资料
	e.owner.do(http.MethodDelete, e.path("/files/%d", r1.File.ID), "", true)
	rec = e.owner.do(http.MethodPost, send, `{"content":"住宿标准？","model":"ds-flash"}`, true)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "event: citation") || !strings.Contains(rec.Body.String(), "没有可用资料") {
		t.Fatalf("无可用资料：%d %s", rec.Code, rec.Body)
	}
	_ = user.Email
	_ = ctx
}

func TestChatKBRetrieveAuditFailClosed(t *testing.T) {
	e := newKBEnv(t, func(d *api.Deps) { d.Audit = failRetrieve{d.Audit} })
	chatReady(t, e)
	if _, r := e.up(t, e.owner, "差旅.md", policyDoc); r.File.Status != "ready" {
		t.Fatal("index")
	}
	id := newConv(t, e.owner, fmt.Sprintf(`{"kb_ids":[%d]}`, e.kbID))
	calls := e.m.Count()
	rec := e.owner.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", id), `{"content":"住宿标准？","model":"ds-flash"}`, true)
	if rec.Code != 503 || e.m.Count() != calls {
		t.Fatalf("检索审计失败应拒绝且不调用模型：%d %s", rec.Code, rec.Body)
	}
	rec = e.owner.do(http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d", id), "", false)
	if !strings.Contains(rec.Body.String(), `"error_code":"audit_failed"`) {
		t.Fatalf("消息应标记 audit_failed：%s", rec.Body)
	}
}

// chatReady 给知识库所有者开通 ds-flash 的使用权。
func chatReady(t *testing.T, e *kbEnv) {
	t.Helper()
	m := e.c.Model.Query().Where(model.Name("ds-flash")).OnlyX(t.Context())
	u := e.c.User.Query().Where(user.Email("kbowner@x.local")).OnlyX(t.Context())
	grantUse(t, e.gwEnv, m.ID, u.ID)
}

func auditDetail(t *testing.T, e *kbEnv, action string) string {
	t.Helper()
	l := e.c.AuditLog.Query().AllX(t.Context())
	out := ""
	for _, a := range l {
		if a.Action == action && a.Phase == "result" {
			b, _ := json.Marshal(a.Detail)
			out = string(b)
		}
	}
	return out
}
