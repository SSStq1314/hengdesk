package api_test

// D-42：嵌入调用不受预算拦截，但计入用量并触发预算告警（索引与检索两条路径）。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/api"
	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/budget"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/model"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
)

func TestKBEmbeddingBudgetAlert(t *testing.T) {
	e := newKBEnv(t)
	chatReady(t, e)
	ctx := t.Context()
	emb := e.c.Model.Query().Where(model.Name("emb")).OnlyX(ctx)
	mk := func(period string) {
		rec := e.admin.do(http.MethodPost, "/api/v1/admin/budgets",
			fmt.Sprintf(`{"scope":"model","scope_id":%d,"period":%q,"metric":"tokens","limit":1,"action":"block"}`, emb.ID, period), true)
		if rec.Code != 201 {
			t.Fatalf("budget %d %s", rec.Code, rec.Body)
		}
	}
	alerts := func() int { return e.c.AuditLog.Query().Where(auditlog.Action(budget.AuditAlert)).CountX(ctx) }
	mk("day")
	// 索引路径：即使预算动作为拦截，嵌入照常执行，用量结算后触发告警
	if _, r := e.up(t, e.owner, "差旅.md", policyDoc); r.File.Status != "ready" {
		t.Fatalf("index %+v", r)
	}
	if n := alerts(); n != 1 {
		t.Fatalf("索引嵌入应触发告警，got %d", n)
	}
	// 检索路径：另建一个月度预算，会话检索的问题嵌入同样结算并告警
	mk("month")
	id := newConv(t, e.owner, fmt.Sprintf(`{"kb_ids":[%d]}`, e.kbID))
	rec := e.owner.do(http.MethodPost, fmt.Sprintf("/api/v1/conversations/%d/messages", id), `{"content":"出差住宿标准是多少？","model":"ds-flash"}`, true)
	if rec.Code != 200 {
		t.Fatalf("send %d %s", rec.Code, rec.Body)
	}
	if n := alerts(); n != 2 {
		t.Fatalf("检索嵌入应触发告警，got %d", n)
	}
}

type failAlertAudit struct {
	audit.Writer
	on *atomic.Bool
}

func (w failAlertAudit) Write(ctx context.Context, e audit.Entry) error {
	if w.on.Load() && e.Action == auth.AlertAction {
		return errors.New("down")
	}
	return w.Writer.Write(ctx, e)
}

// D-42：超级管理员给自己授予知识库权限写高风险审计；给他人授权不告警；告警写失败拒绝执行。
func TestKBSuperAdminSelfGrantAlert(t *testing.T) {
	var down atomic.Bool
	e := newKBEnv(t, func(d *api.Deps) {
		w := failAlertAudit{d.Audit, &down}
		d.Audit = w
		if d.Auth != nil {
			d.Auth.Audit = w
		}
	})
	ctx := t.Context()
	adminU := e.c.User.Query().Where(user.Email("admin@x.local")).OnlyX(ctx)
	member := e.c.User.Query().Where(user.Email("member@x.local")).OnlyX(ctx)
	alerts := func() int {
		n := 0
		for _, r := range e.c.AuditLog.Query().Where(auditlog.Action(auth.AlertAction)).AllX(ctx) {
			if r.Detail["kind"] == auth.AlertKBSelfGrant {
				n++
			}
		}
		return n
	}
	grant := func(uid int) int {
		return e.admin.do(http.MethodPost, e.path("/acl"), fmt.Sprintf(`{"subject_type":"user","subject_id":%d,"perm":"view"}`, uid), true).Code
	}
	// 所有者给超管授权、超管给成员授权：不告警
	if rec := e.owner.do(http.MethodPost, e.path("/acl"), fmt.Sprintf(`{"subject_type":"user","subject_id":%d,"perm":"manage"}`, adminU.ID), true); rec.Code != 204 {
		t.Fatalf("owner grant %d %s", rec.Code, rec.Body)
	}
	if c := grant(member.ID); c != 204 || alerts() != 0 {
		t.Fatalf("给他人 %d alerts=%d", c, alerts())
	}
	// 告警写失败：拒绝，权限不变
	down.Store(true)
	if c := grant(adminU.ID); c == 204 {
		t.Fatal("告警写失败应拒绝")
	}
	down.Store(false)
	if c := grant(adminU.ID); c != 204 || alerts() != 1 {
		t.Fatalf("给自己 %d alerts=%d", c, alerts())
	}
}
