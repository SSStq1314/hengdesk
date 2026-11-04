package api_test

// M1 第四块 PR2：Retriever 走真实 ACL（D-14 / D-27）：只检索可见且就绪的文件；文件自定义权限即时生效；
// 每次检索写 kb.retrieve 审计（不含问题原文与片段内容），审计失败拒绝返回结果。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/kb"
)

type failRetrieve struct{ audit.Writer }

func (w failRetrieve) Write(ctx context.Context, e audit.Entry) error {
	if e.Action == "kb.retrieve" {
		return errors.New("down")
	}
	return w.Writer.Write(ctx, e)
}

func TestKBRetrieveACL(t *testing.T) {
	e := newKBEnv(t)
	ctx := context.Background()
	_, r1 := e.up(t, e.owner, "差旅.md", policyDoc)
	_, r2 := e.up(t, e.owner, "保密.md", "# 薪酬\n\n高管薪酬方案：住宿标准另行规定，保密。\n")
	if r1.File.Status != "ready" || r2.File.Status != "ready" {
		t.Fatalf("index %+v %+v", r1, r2)
	}
	member := e.c.User.Query().Where(user.Email("member@x.local")).OnlyX(ctx)
	ownerU := e.c.User.Query().Where(user.Email("kbowner@x.local")).OnlyX(ctx)
	rt := &kb.Retriever{DB: e.c, AZ: &authz.Service{DB: e.c}, Store: e.store, GW: e.gw, Audit: audit.EntWriter{C: e.c}}
	ret := func(uid int, w audit.Writer) (*kb.Result, error) {
		p, err := auth.LoadPrincipal(ctx, e.c, uid)
		if err != nil {
			t.Fatal(err)
		}
		r := *rt
		if w != nil {
			r.Audit = w
		}
		return r.Retrieve(ctx, p, []int{e.kbID}, "住宿标准是多少", "req-1", 8)
	}
	files := func(res *kb.Result) string {
		var s []string
		for _, c := range res.Citations {
			s = append(s, c.FileName)
		}
		return strings.Join(s, ",")
	}
	// 无授权：0 可见文件，不调用 embedding
	before := e.m.EmbedCalls
	res, err := ret(member.ID, nil)
	if err != nil || res.AllowedFiles != 0 || len(res.Citations) != 0 || e.m.EmbedCalls != before {
		t.Fatalf("no access: %+v %v", res, err)
	}
	// 所有者：两份都能命中，第一条是差旅制度
	res, err = ret(ownerU.ID, nil)
	if err != nil || res.AllowedFiles != 2 || !strings.Contains(files(res), "差旅.md") || res.Citations[0].Index != 1 {
		t.Fatalf("owner: %+v %v", res, err)
	}
	// 成员获 view，保密文件改为自定义 → 只命中差旅
	e.owner.do("POST", e.path("/acl"), fmt.Sprintf(`{"subject_type":"user","subject_id":%d,"perm":"view"}`, member.ID), true)
	e.owner.do("PUT", e.path("/files/%d/acl-mode", r2.File.ID), `{"custom":true}`, true)
	res, err = ret(member.ID, nil)
	if err != nil || res.AllowedFiles != 1 || strings.Contains(files(res), "保密") || len(res.Citations) == 0 {
		t.Fatalf("member: %s %+v %v", files(res), res, err)
	}
	// 审计：记 ID 与策略，不记问题原文与片段
	l := e.c.AuditLog.Query().Where(auditlog.Action("kb.retrieve"), auditlog.ActorID(member.ID)).AllX(ctx)
	if len(l) != 2 {
		t.Fatalf("retrieve audits %d", len(l))
	}
	b, _ := json.Marshal(l[len(l)-1].Detail)
	if strings.Contains(string(b), "住宿") || !strings.Contains(string(b), `"strategy":"exact"`) || !strings.Contains(string(b), `"file_id"`) {
		t.Fatalf("audit detail %s", b)
	}
	// 审计失败 → 拒绝
	if _, err := ret(member.ID, failRetrieve{audit.EntWriter{C: e.c}}); !errors.Is(err, kb.ErrRetrieveAudit) {
		t.Fatalf("audit fail-closed: %v", err)
	}
}
