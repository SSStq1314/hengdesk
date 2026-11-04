package api_test

// M1 第四块 PR1：知识库 CRUD、ACL（含文件级）、上传与异步索引（测试中同步执行）、去重、失败重试、删除、审计 fail-closed。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/api"
	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent/acl"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/kbfile"
	"github.com/SSStq1314/hengdesk/server/internal/ent/usagerecord"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/kb"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

type failKBUpload struct{ audit.Writer }

func (w failKBUpload) Write(ctx context.Context, e audit.Entry) error {
	if e.Action == "kb.file.upload" && e.Phase == "attempt" {
		return errors.New("audit store down")
	}
	return w.Writer.Write(ctx, e)
}

type kbEnv struct {
	*gwEnv
	objs  *memStore
	store *kb.MemStore
	owner *client
	kbID  int
	gw    *gateway.Service
}

func newKBEnv(t *testing.T, opts ...func(*api.Deps)) *kbEnv {
	objs := &memStore{m: map[string][]byte{}}
	ms := kb.NewMemStore()
	var gws *gateway.Service
	g := newGW(t, false, append([]func(*api.Deps){func(d *api.Deps) {
		d.Files, d.KBStore, gws = objs, ms, d.Gateway
	}}, opts...)...)
	for _, m := range []struct{ name, up string }{{"emb", "mock-embed"}, {"emb768", "mock-embed-768"}} {
		pid := g.c.ModelProvider.Query().OnlyX(context.Background()).ID
		rec := g.admin.do(http.MethodPost, "/api/v1/admin/models",
			fmt.Sprintf(`{"name":%q,"provider_id":%d,"upstream_model":%q}`, m.name, pid, m.up), true)
		if rec.Code != 201 {
			t.Fatalf("create model %d %s", rec.Code, rec.Body)
		}
	}
	org := g.c.Organization.Query().FirstX(context.Background())
	g.addUser(t, "kbowner@x.local", org, rbac.OrgAdmin, nil)
	e := &kbEnv{gwEnv: g, objs: objs, store: ms, owner: g.loginAs(t, "kbowner@x.local", "password123"), gw: gws}
	rec := e.owner.do(http.MethodPost, "/api/v1/kbs", `{"name":"制度库","description":"公司制度","embedding_model":"emb"}`, true)
	if rec.Code != 201 {
		t.Fatalf("create kb %d %s", rec.Code, rec.Body)
	}
	var v struct {
		ID    int
		Perms []string
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if strings.Join(v.Perms, ",") != "view,use,edit,manage" {
		t.Fatalf("creator perms %v", v.Perms)
	}
	e.kbID = v.ID
	return e
}

type upResp struct {
	Duplicate bool
	Message   string
	File      struct {
		ID         int
		Name       string
		Status     string
		ErrorCode  string `json:"error_code"`
		ChunkCount int    `json:"chunk_count"`
	}
}

func (e *kbEnv) up(t *testing.T, cl *client, name, body string) (int, upResp) {
	t.Helper()
	rec := cl.upload(fmt.Sprintf("/api/v1/kbs/%d/files", e.kbID), name, []byte(body))
	var r upResp
	_ = json.Unmarshal(rec.Body.Bytes(), &r)
	return rec.Code, r
}

func (e *kbEnv) path(f string, a ...any) string {
	return fmt.Sprintf("/api/v1/kbs/%d", e.kbID) + fmt.Sprintf(f, a...)
}

const policyDoc = "# 差旅制度\n\n员工出差住宿标准为每晚不超过 500 元。\n\n# 报销流程\n\n报销需在出差结束后 30 天内提交发票。\n"

func TestKBUploadIndexDedupAndACL(t *testing.T) {
	e := newKBEnv(t)
	ctx := context.Background()
	member := e.loginAs(t, "member@x.local", "password123")
	memberID := e.c.User.Query().AllX(ctx)[1].ID

	// 未登录 401；没有授权的成员看不到、传不了（404）
	if got := get(e.h, e.path(""), nil); got != 401 {
		t.Fatalf("anon %d", got)
	}
	for _, r := range []struct{ m, p string }{{"GET", ""}, {"GET", "/files"}, {"PATCH", ""}, {"DELETE", ""}, {"GET", "/acl"}} {
		if got := member.do(r.m, e.path("%s", r.p), `{}`, true).Code; got != 404 {
			t.Errorf("member %s %s: %d", r.m, r.p, got)
		}
	}
	if code, _ := e.up(t, member, "a.txt", "x"); code != 404 {
		t.Fatalf("member upload %d", code)
	}
	if rec := member.do("GET", "/api/v1/kbs", "", false); rec.Code != 200 || strings.Contains(rec.Body.String(), "制度库") {
		t.Fatalf("member list %d %s", rec.Code, rec.Body)
	}

	// 上传 → 同步索引完成
	code, r := e.up(t, e.owner, "差旅.md", policyDoc)
	if code != 201 || r.File.Status != "ready" || r.File.ChunkCount == 0 {
		t.Fatalf("upload %d %+v", code, r)
	}
	f1 := r.File.ID
	if n, _ := e.store.CountFile(ctx, f1); n != r.File.ChunkCount {
		t.Fatalf("store chunks %d vs %d", n, r.File.ChunkCount)
	}
	if e.m.EmbedCalls == 0 {
		t.Fatal("没有调用 /embeddings")
	}
	if n := e.c.UsageRecord.Query().Where(usagerecord.PurposeEQ(usagerecord.PurposeEmbedding)).CountX(ctx); n != 1 {
		t.Fatalf("embedding usage records %d", n)
	}
	// 相同内容去重；同名不同内容改名
	code, r = e.up(t, e.owner, "另一个名字.md", policyDoc)
	if code != 200 || !r.Duplicate || r.File.ID != f1 || !strings.Contains(r.Message, "差旅.md") {
		t.Fatalf("dup %d %+v", code, r)
	}
	code, r = e.up(t, e.owner, "差旅.md", "新版差旅制度：住宿 600 元。")
	if code != 201 || r.File.Name != "差旅 (2).md" || r.Message == "" {
		t.Fatalf("rename %d %+v", code, r)
	}
	f2 := r.File.ID
	// 不支持的格式、空文本（扫描件）
	if code, _ := e.up(t, e.owner, "a.exe", "MZ\x00"); code != 415 {
		t.Fatalf("exe %d", code)
	}
	code, r = e.up(t, e.owner, "空白.txt", "   \n\n  ")
	if code != 201 || r.File.Status != "failed" || r.File.ErrorCode != kb.ErrCodeNoText {
		t.Fatalf("empty %d %+v", code, r)
	}

	// 授予成员 view：可看、可列文件，但不能上传 / 删除 / 管理（404）
	if rec := e.owner.do("POST", e.path("/acl"), fmt.Sprintf(`{"subject_type":"user","subject_id":%d,"perm":"view"}`, memberID), true); rec.Code != 204 {
		t.Fatalf("grant %d %s", rec.Code, rec.Body)
	}
	if rec := member.do("GET", e.path(""), "", false); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"perms":["view"]`) {
		t.Fatalf("member get %d %s", rec.Code, rec.Body)
	}
	if code, _ := e.up(t, member, "b.txt", "hello"); code != 404 {
		t.Fatalf("viewer upload %d", code)
	}
	if got := member.do("DELETE", e.path("/files/%d", f1), "", true).Code; got != 404 {
		t.Fatalf("viewer delete %d", got)
	}
	if got := member.do("GET", e.path("/acl"), "", false).Code; got != 404 {
		t.Fatalf("viewer acl %d", got)
	}
	listIDs := func(cl *client) string {
		rec := cl.do("GET", e.path("/files"), "", false)
		var v struct{ Items []struct{ ID int } }
		_ = json.Unmarshal(rec.Body.Bytes(), &v)
		var s []string
		for _, it := range v.Items {
			s = append(s, fmt.Sprint(it.ID))
		}
		return strings.Join(s, ",")
	}
	if got := listIDs(member); !strings.Contains(got, fmt.Sprint(f1)) {
		t.Fatalf("member files %s", got)
	}

	// 文件级 ACL：f1 设为自定义 → 成员看不到（列表与单文件接口 404）；单独授予 view 后可见
	if rec := e.owner.do("PUT", e.path("/files/%d/acl-mode", f1), `{"custom":true}`, true); rec.Code != 200 {
		t.Fatalf("acl-mode %d %s", rec.Code, rec.Body)
	}
	if got := listIDs(member); strings.Contains(","+got+",", fmt.Sprintf(",%d,", f1)) {
		t.Fatalf("custom file leaked: %s", got)
	}
	if rec := e.owner.do("POST", e.path("/files/%d/acl", f1), fmt.Sprintf(`{"subject_type":"user","subject_id":%d,"perm":"view"}`, memberID), true); rec.Code != 204 {
		t.Fatalf("file grant %d %s", rec.Code, rec.Body)
	}
	if got := listIDs(member); !strings.Contains(got, fmt.Sprint(f1)) {
		t.Fatalf("file grant not effective: %s", got)
	}
	if rec := e.owner.do("POST", e.path("/files/%d/acl", f1), fmt.Sprintf(`{"subject_type":"user","subject_id":%d,"perm":"edit"}`, memberID), true); rec.Code != 400 {
		t.Fatalf("file grant edit %d", rec.Code)
	}
	// 不能撤销最后一个 manage
	rows := e.c.ACL.Query().Where(acl.ResourceType("kb"), acl.ResourceID(fmt.Sprint(e.kbID)), acl.PermsGTE(rbac.BitManage)).AllX(ctx)
	if len(rows) != 1 {
		t.Fatalf("manage rows %d", len(rows))
	}
	if got := e.owner.do("DELETE", e.path("/acl/user/%d", rows[0].SubjectID), "", true).Code; got != 409 {
		t.Fatalf("revoke last manage %d", got)
	}

	// 删除文件：切片、原件、文件 ACL 一并删除
	objsBefore := e.objs.n()
	if got := e.owner.do("DELETE", e.path("/files/%d", f1), "", true).Code; got != 204 {
		t.Fatalf("delete file %d", got)
	}
	if n, _ := e.store.CountFile(ctx, f1); n != 0 || e.objs.n() != objsBefore-1 {
		t.Fatalf("chunks %d objs %d→%d", n, objsBefore, e.objs.n())
	}
	if e.c.ACL.Query().Where(acl.ResourceType(rbac.ResKBFile)).CountX(ctx) != 0 {
		t.Fatal("文件 ACL 未删除")
	}
	// 删除知识库：之后 404，文件与 ACL 清空
	if got := e.owner.do("DELETE", e.path(""), "", true).Code; got != 204 {
		t.Fatalf("delete kb %d", got)
	}
	if got := e.owner.do("GET", e.path(""), "", false).Code; got != 404 {
		t.Fatalf("deleted kb %d", got)
	}
	if e.c.KBFile.Query().CountX(ctx) != 0 || e.store.Total() != 0 || e.c.ACL.Query().Where(acl.ResourceType("kb")).CountX(ctx) != 0 {
		t.Fatal("知识库删除后仍有残留")
	}
	_ = f2
	for _, act := range []string{"kb.create", "kb.file.upload", "kb.acl.grant", "kb.file.acl.mode", "kb.file.delete", "kb.delete", "authz.denied"} {
		if e.c.AuditLog.Query().Where(auditlog.Action(act)).CountX(ctx) == 0 {
			t.Errorf("缺少审计 %s", act)
		}
	}
}

func TestKBRetryAndModelChange(t *testing.T) {
	e := newKBEnv(t)
	ctx := context.Background()
	e.m.EmbedFail = 500
	code, r := e.up(t, e.owner, "a.txt", "第一段内容。")
	if code != 201 || r.File.Status != "failed" || r.File.ErrorCode != kb.ErrCodeEmbed {
		t.Fatalf("embed fail %d %+v", code, r)
	}
	if n := e.c.UsageRecord.Query().Where(usagerecord.PurposeEQ(usagerecord.PurposeEmbedding), usagerecord.ErrorCodeNEQ("")).CountX(ctx); n != 1 {
		t.Fatalf("failed embedding usage %d", n)
	}
	e.m.EmbedFail = 0
	rec := e.owner.do("POST", e.path("/files/%d/retry", r.File.ID), "", true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"ready"`) {
		t.Fatalf("retry %d %s", rec.Code, rec.Body)
	}
	if got := e.owner.do("POST", e.path("/files/%d/retry", r.File.ID), "", true).Code; got != 409 {
		t.Fatalf("retry ready %d", got)
	}
	// 切片参数校验
	if got := e.owner.do("PATCH", e.path(""), `{"chunk_size":100}`, true).Code; got != 400 {
		t.Fatalf("bad chunk %d", got)
	}
	// 换成 768 维模型：全部文件重新排队，维度不符失败
	if rec := e.owner.do("PATCH", e.path(""), `{"embedding_model":"emb768"}`, true); rec.Code != 200 {
		t.Fatalf("change model %d %s", rec.Code, rec.Body)
	}
	f := e.c.KBFile.GetX(ctx, r.File.ID)
	if f.Status != kbfile.StatusFailed || f.ErrorCode != kb.ErrCodeDimMismatch {
		t.Fatalf("dim mismatch %s %s", f.Status, f.ErrorCode)
	}
	if got := e.owner.do("PATCH", e.path(""), `{"embedding_model":"nope"}`, true).Code; got != 400 {
		t.Fatalf("unknown model %d", got)
	}
	if rec := e.owner.do("PATCH", e.path(""), `{"embedding_model":"emb","chunk_size":400,"chunk_overlap":50}`, true); rec.Code != 200 {
		t.Fatalf("change back %d %s", rec.Code, rec.Body)
	}
	if f := e.c.KBFile.GetX(ctx, r.File.ID); f.Status != kbfile.StatusReady || f.EmbeddingModel != "emb" {
		t.Fatalf("reindex %s %s", f.Status, f.EmbeddingModel)
	}
}

func TestKBUploadAuditFailClosed(t *testing.T) {
	e := newKBEnv(t, func(d *api.Deps) { d.Audit = failKBUpload{d.Audit} })
	code, _ := e.up(t, e.owner, "a.txt", "内容")
	if code < 500 {
		t.Fatalf("want 5xx, got %d", code)
	}
	if e.c.KBFile.Query().CountX(context.Background()) != 0 || e.objs.n() != 0 {
		t.Fatal("审计失败仍写入了文件")
	}
}

func TestKBCreatePerm(t *testing.T) {
	e := newKBEnv(t)
	member := e.loginAs(t, "member@x.local", "password123")
	rec := member.do("GET", "/api/v1/kbs", "", false)
	if rec.Code != 200 {
		t.Fatalf("list %d", rec.Code)
	}
	if got := e.owner.do("POST", "/api/v1/kbs", `{"name":"x","embedding_model":"nope"}`, true).Code; got != 400 {
		t.Fatalf("bad model %d", got)
	}
	if got := e.owner.do("POST", "/api/v1/kbs", `{"name":"","embedding_model":"emb"}`, true).Code; got != 400 {
		t.Fatalf("empty name %d", got)
	}
}
