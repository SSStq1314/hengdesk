package api_test

// M1 第三块 PR2：会话内上传文件（FR-15）。只有会话主人可上传 / 查看 / 下载 / 删除（其他人 404）；
// 类型按内容判定；大小上限；审计只记名称、大小、哈希且 fail-closed；文件文本进入对话上下文；删除会话时文件一并软删除。
// 另补 PR1 漏测：对话接口的预算超额拦截。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/api"
	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/conversationfile"
	"github.com/SSStq1314/hengdesk/server/internal/ent/message"
	"github.com/SSStq1314/hengdesk/server/internal/ent/model"
)

type memStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memStore) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = b
	return nil
}

func (s *memStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[key]
	if !ok {
		return nil, errors.New("no such key")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (s *memStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}

func (s *memStore) n() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

func (cl *client) upload(path, name string, data []byte) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	w, _ := mw.CreateFormFile("file", name)
	_, _ = w.Write(data)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set(auth.CSRFHeader, cl.csrf)
	for _, ck := range cl.cks {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	cl.h.ServeHTTP(rec, req)
	return rec
}

type failFileAudit struct{ audit.Writer }

func (w failFileAudit) Write(ctx context.Context, e audit.Entry) error {
	if strings.HasPrefix(e.Action, "chat.file.") && e.Phase == "attempt" {
		return errors.New("audit store down")
	}
	return w.Writer.Write(ctx, e)
}

func newFileEnv(t *testing.T, failAudit bool) (*gwEnv, *memStore) {
	st := &memStore{m: map[string][]byte{}}
	g := newGW(t, false, func(d *api.Deps) {
		d.Files = st
		d.FileLimits = api.FileLimits{MaxBytes: 64 << 10, MaxPerConv: 3, FileChars: 300, TotalChars: 500}
		if failAudit {
			d.Audit = failFileAudit{d.Audit}
		}
	})
	return g, st
}

func testDocx(t *testing.T) []byte {
	b, err := os.ReadFile("../docparse/testdata/zh_report.docx")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestConversationFilesOwnerOnlyAndContext(t *testing.T) {
	g, st := newFileEnv(t, false)
	ctx := context.Background()
	primary := g.c.Model.Query().Where(model.Name("ds-flash")).OnlyIDX(ctx)
	grantUse(t, g, primary, atoi(g.uid(t, "member@x.local")))
	mem := g.loginAs(t, "member@x.local", "password123")
	id := newConv(t, mem, `{}`)
	base := fmt.Sprintf("/api/v1/conversations/%d/files", id)

	docx := testDocx(t)
	rec := mem.upload(base, "../../季度报告.docx", docx)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"name":"季度报告.docx"`) || !strings.Contains(rec.Body.String(), `"kind":"docx"`) {
		t.Fatalf("upload %d %s", rec.Code, rec.Body)
	}
	// 文本 300 字上限：报告约 400 字，应截断
	if !strings.Contains(rec.Body.String(), `"truncated":true`) {
		t.Fatalf("应标记截断 %s", rec.Body)
	}
	f := g.c.ConversationFile.Query().OnlyX(ctx)
	if st.n() != 1 || !strings.HasPrefix(f.ObjectKey, "conv/") || f.Sha256 == "" {
		t.Fatalf("应写入对象存储 %+v", f)
	}
	fileURL := fmt.Sprintf("%s/%d", base, f.ID)

	// 他人（含超级管理员）一律 404；不存在的文件也 404
	for _, r := range []struct{ m, p string }{
		{http.MethodGet, base}, {http.MethodGet, fileURL + "/content"}, {http.MethodDelete, fileURL},
	} {
		if rec := g.admin.do(r.m, r.p, "", true); rec.Code != 404 {
			t.Fatalf("%s %s 非本人应 404：%d", r.m, r.p, rec.Code)
		}
	}
	if rec := g.admin.upload(base, "a.txt", []byte("hi")); rec.Code != 404 {
		t.Fatalf("他人上传应 404：%d", rec.Code)
	}
	if rec := mem.do(http.MethodGet, fmt.Sprintf("%s/%d/content", base, f.ID+999), "", false); rec.Code != 404 {
		t.Fatalf("不存在应 404：%d", rec.Code)
	}
	// 其他会话下拿不到这个文件
	other := newConv(t, mem, `{}`)
	if rec := mem.do(http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d/files/%d/content", other, f.ID), "", false); rec.Code != 404 {
		t.Fatalf("跨会话应 404：%d", rec.Code)
	}

	// 下载：后端代理，强制附件、nosniff，内容一致，有审计（只记名称 / 大小 / 哈希）
	rec = mem.do(http.MethodGet, fileURL+"/content", "", false)
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), docx) || rec.Header().Get("Content-Type") != "application/octet-stream" ||
		!strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download %d %v", rec.Code, rec.Header())
	}
	for _, a := range []string{"chat.file.upload", "chat.file.download"} {
		l := g.c.AuditLog.Query().Where(auditlog.Action(a), auditlog.PhaseEQ(auditlog.PhaseAttempt)).OnlyX(ctx)
		if l.Detail["sha256"] != f.Sha256 || l.Detail["name"] != "季度报告.docx" {
			t.Fatalf("%s 审计应记名称和哈希 %v", a, l.Detail)
		}
		for k := range l.Detail {
			if k == "text" || k == "content" {
				t.Fatalf("审计不应记内容 %v", l.Detail)
			}
		}
	}

	// 文件文本作为上下文发给模型；done 事件带截断提示
	rec = mem.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", id), `{"content":"总结一下","model":"ds-flash"}`, true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"context_truncated":true`) {
		t.Fatalf("send %d %s", rec.Code, rec.Body)
	}
	msgs, _ := g.m.Requests[len(g.m.Requests)-1]["messages"].([]any)
	sys, _ := msgs[0].(map[string]any)
	if sys["role"] != "system" || !strings.Contains(fmt.Sprint(sys["content"]), "衡台知识库季度运营报告") || !strings.Contains(fmt.Sprint(sys["content"]), "已截断") {
		t.Fatalf("文件内容应进入上下文 %v", msgs)
	}
	if rec := mem.do(http.MethodGet, fmt.Sprintf("/api/v1/conversations/%d", id), "", false); !strings.Contains(rec.Body.String(), `"files":[{`) {
		t.Fatalf("会话详情应带文件列表 %s", rec.Body)
	}

	// 删除文件：软删除，之后 404，且不再进入上下文
	if rec := mem.do(http.MethodDelete, fileURL, "", true); rec.Code != 204 {
		t.Fatalf("delete %d", rec.Code)
	}
	if rec := mem.do(http.MethodGet, fileURL+"/content", "", false); rec.Code != 404 {
		t.Fatalf("删除后应 404：%d", rec.Code)
	}
	if g.c.AuditLog.Query().Where(auditlog.Action("chat.file.delete")).CountX(ctx) == 0 {
		t.Fatal("删除应记审计")
	}
	mem.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", id), `{"content":"再问","model":"ds-flash"}`, true)
	msgs, _ = g.m.Requests[len(g.m.Requests)-1]["messages"].([]any)
	if m0, _ := msgs[0].(map[string]any); m0["role"] == "system" {
		t.Fatalf("已删除文件不应进入上下文 %v", msgs)
	}

	// 删除会话：文件一并软删除
	if rec := mem.upload(base, "b.md", []byte("# 标题\n正文")); rec.Code != 201 {
		t.Fatalf("upload md %d %s", rec.Code, rec.Body)
	}
	if rec := mem.do(http.MethodDelete, fmt.Sprintf("/api/v1/conversations/%d", id), "", true); rec.Code != 204 {
		t.Fatalf("delete conv %d", rec.Code)
	}
	if n := g.c.ConversationFile.Query().Where(conversationfile.DeletedAtIsNil()).CountX(ctx); n != 0 {
		t.Fatalf("删除会话应软删除文件 %d", n)
	}
	if n := g.c.ConversationFile.Query().CountX(ctx); n != 2 {
		t.Fatalf("软删除应保留记录 %d", n)
	}
}

func TestConversationFileValidation(t *testing.T) {
	g, st := newFileEnv(t, false)
	id := newConv(t, g.admin, `{}`)
	base := fmt.Sprintf("/api/v1/conversations/%d/files", id)
	docx := testDocx(t)
	for _, c := range []struct {
		name string
		data []byte
		code int
	}{
		{"a.exe", []byte("MZ\x90\x00"), 415},
		{"假装.pdf", docx, 415},                                   // 扩展名是 pdf，内容是 docx
		{"假装.txt", []byte("%PDF-1.4\n%\xe2\xe3\n1 0 obj"), 415}, // 扩展名是 txt，内容是 PDF
		{"bin.txt", []byte{0, 1, 2, 3}, 415},
		{"big.txt", bytes.Repeat([]byte("a"), 64<<10+1), 413},
		{"empty.txt", []byte("   \n"), 422},
		{"坏.pdf", []byte("%PDF-1.4\ngarbage"), 422},
	} {
		if rec := g.admin.upload(base, c.name, c.data); rec.Code != c.code {
			t.Fatalf("%s 应 %d：%d %s", c.name, c.code, rec.Code, rec.Body)
		}
	}
	if st.n() != 0 || g.c.ConversationFile.Query().CountX(context.Background()) != 0 {
		t.Fatal("被拒绝的文件不应落盘")
	}
	// 每个会话文件数上限
	for i := 0; i < 3; i++ {
		if rec := g.admin.upload(base, fmt.Sprintf("%d.txt", i), []byte("内容")); rec.Code != 201 {
			t.Fatalf("upload %d %s", rec.Code, rec.Body)
		}
	}
	if rec := g.admin.upload(base, "4.txt", []byte("内容")); rec.Code != 409 {
		t.Fatalf("超过文件数应 409：%d", rec.Code)
	}
}

func TestConversationFileAuditFailClosed(t *testing.T) {
	g, st := newFileEnv(t, true)
	id := newConv(t, g.admin, `{}`)
	rec := g.admin.upload(fmt.Sprintf("/api/v1/conversations/%d/files", id), "a.txt", []byte("hello"))
	if rec.Code < 500 || st.n() != 0 || g.c.ConversationFile.Query().CountX(context.Background()) != 0 {
		t.Fatalf("审计写失败应拒绝上传：%d %d", rec.Code, st.n())
	}
}

func TestConversationFilesNoStore(t *testing.T) {
	g := newGW(t, false)
	id := newConv(t, g.admin, `{}`)
	if rec := g.admin.upload(fmt.Sprintf("/api/v1/conversations/%d/files", id), "a.txt", []byte("hello")); rec.Code != 503 {
		t.Fatalf("未配置对象存储应 503：%d", rec.Code)
	}
}

// PR1 漏测：对话接口同样受预算拦截（429 budget_exceeded），不调用上游、不写消息。
func TestConversationBudgetBlocked(t *testing.T) {
	f := newBudgetFixture(t)
	ctx := context.Background()
	id := newConv(t, f.admin, `{}`)
	send := fmt.Sprintf("/api/v1/conversations/%d/messages", id)
	if rec := f.admin.do(http.MethodPost, send, `{"content":"hi"}`, true); rec.Code != 200 {
		t.Fatalf("send %d %s", rec.Code, rec.Body)
	}
	if rec := f.admin.do(http.MethodPost, "/api/v1/admin/budgets", `{"scope":"org","period":"day","metric":"tokens","limit":1}`, true); rec.Code != 201 {
		t.Fatalf("create budget %d %s", rec.Code, rec.Body)
	}
	calls, msgs := f.m.Count(), f.c.Message.Query().CountX(ctx)
	rec := f.admin.do(http.MethodPost, send, `{"content":"再来"}`, true)
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), `"code":"budget_exceeded"`) {
		t.Fatalf("应拦截 %d %s", rec.Code, rec.Body)
	}
	if f.m.Count() != calls || f.c.Message.Query().CountX(ctx) != msgs {
		t.Fatal("被拦截时不应调用上游或写消息")
	}
	if n := f.c.AuditLog.Query().Where(auditlog.Action("model.call"), auditlog.Outcome("denied")).CountX(ctx); n != 1 {
		t.Fatalf("拦截应记审计 %d", n)
	}
	if f.c.Message.Query().Where(message.StatusEQ(message.StatusStreaming)).CountX(ctx) != 0 {
		t.Fatal("不应残留 streaming 消息")
	}
}
