package chatretention_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/chatretention"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/store/storetest"
)

type objs struct {
	mu   sync.Mutex
	gone []string
}

func (o *objs) Delete(_ context.Context, k string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.gone = append(o.gone, k)
	return nil
}

type failAttempt struct{ audit.Writer }

func (w failAttempt) Write(ctx context.Context, e audit.Entry) error {
	if e.Phase == "attempt" {
		return errors.New("audit down")
	}
	return w.Writer.Write(ctx, e)
}

type fx struct {
	c         *ent.Client
	now       time.Time
	org, org2 int
	u1, u2    int
}

func (f *fx) conv(t *testing.T, org, uid int, created, lastMsg time.Time, deleted bool) int {
	ctx := context.Background()
	q := f.c.Conversation.Create().SetOrgID(org).SetUserID(uid).SetCreatedAt(created)
	if deleted {
		q.SetDeletedAt(lastMsg.Add(time.Hour))
	}
	cv := q.SaveX(ctx)
	f.c.Message.Create().SetConversationID(cv.ID).SetRole("user").SetContent("问").SetCreatedAt(created).ExecX(ctx)
	f.c.Message.Create().SetConversationID(cv.ID).SetRole("assistant").SetContent("答").SetCreatedAt(lastMsg).ExecX(ctx)
	f.c.ConversationFile.Create().SetOrgID(org).SetConversationID(cv.ID).SetUserID(uid).SetName("a.txt").SetKind("txt").
		SetSize(1).SetSha256("x").SetObjectKey("k/" + time.Now().Format("150405.000000000")).ExecX(ctx)
	return cv.ID
}

func newFx(t *testing.T) *fx {
	c := storetest.NewTestClient(t)
	ctx := context.Background()
	o1 := c.Organization.Create().SetName("o1").SetSlug("o1").SaveX(ctx)
	o2 := c.Organization.Create().SetName("o2").SetSlug("o2").SaveX(ctx)
	u1 := c.User.Create().SetEmail("u1@x").SetOrg(o1).SaveX(ctx)
	u2 := c.User.Create().SetEmail("u2@x").SetOrg(o1).SaveX(ctx)
	return &fx{c: c, now: time.Now(), org: o1.ID, org2: o2.ID, u1: u1.ID, u2: u2.ID}
}

func TestSettingsDefaultsAndMinimum(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	st, err := chatretention.Load(ctx, f.c)
	if err != nil || st.Enabled || st.Days != 365 {
		t.Fatalf("默认应不开启、365 天：%+v %v", st, err)
	}
	if err := chatretention.Save(ctx, f.c, chatretention.Settings{Enabled: true, Days: 179}, 1); err == nil {
		t.Fatal("低于 180 天应拒绝")
	}
	if err := chatretention.Save(ctx, f.c, chatretention.Settings{Enabled: true, Days: 180}, 1); err != nil {
		t.Fatal(err)
	}
	if st, _ := chatretention.Load(ctx, f.c); !st.Enabled || st.Days != 180 {
		t.Fatalf("%+v", st)
	}
}

func TestPurge(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	w := audit.EntWriter{C: f.c}
	op := chatretention.Operator{Email: "system", Trigger: "scheduled"}
	old := f.now.AddDate(0, 0, -400)
	recent := f.now.AddDate(0, 0, -10)

	if _, err := chatretention.Purge(ctx, f.c, w, nil, op, f.now); !errors.Is(err, chatretention.ErrDisabled) {
		t.Fatalf("默认不开启：%v", err)
	}
	oldConv := f.conv(t, f.org, f.u1, old, old, false)
	oldDeleted := f.conv(t, f.org, f.u1, old, old, true)
	// 软删除发生在近期也一样按最后活动时间清理（不因软删除推后）
	oldDeletedLate := f.conv(t, f.org, f.u1, old, old, false)
	f.c.Conversation.UpdateOneID(oldDeletedLate).SetDeletedAt(recent).ExecX(ctx)
	active := f.conv(t, f.org, f.u1, old, recent, false) // 很早创建、最近还在聊
	fresh := f.conv(t, f.org, f.u1, recent, recent, false)
	held := f.conv(t, f.org, f.u2, old, old, false)
	other := f.conv(t, f.org2, f.u1, old, old, false)
	f.c.LegalHold.Create().SetOrgID(f.org).SetUserID(f.u2).SetReason("诉讼").SetCreatedBy(f.u1).ExecX(ctx)
	if err := chatretention.Save(ctx, f.c, chatretention.Settings{Enabled: true, Days: 365}, 1); err != nil {
		t.Fatal(err)
	}

	// 审计 attempt 写失败：不删除任何数据
	if _, err := chatretention.Purge(ctx, f.c, failAttempt{w}, nil, op, f.now); !errors.Is(err, chatretention.ErrAudit) {
		t.Fatalf("审计失败应拒绝：%v", err)
	}
	if n := f.c.Conversation.Query().CountX(ctx); n != 7 {
		t.Fatalf("审计失败不应删除：%d", n)
	}

	o := &objs{}
	res, err := chatretention.Purge(ctx, f.c, w, o, op, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Conversations != 4 || res.Messages != 8 || res.Files != 4 || len(o.gone) != 4 || res.HeldUsers != 1 {
		t.Fatalf("清理结果 %+v objs=%d", res, len(o.gone))
	}
	for _, id := range []int{oldConv, oldDeleted, oldDeletedLate, other} {
		if existsConv(f.c, id) {
			t.Fatalf("会话 %d 应被清理", id)
		}
	}
	for _, id := range []int{active, fresh, held} {
		if !existsConv(f.c, id) {
			t.Fatalf("会话 %d 不应被清理", id)
		}
	}
	r := f.c.AuditLog.Query().Where(auditlog.Action(chatretention.Action), auditlog.PhaseEQ(auditlog.PhaseResult)).OnlyX(ctx)
	if r.ActorEmail != "system" || r.Detail["conversations"].(float64) != 4 || r.Detail["messages"].(float64) != 8 ||
		r.Detail["from"] == nil || r.Detail["to"] == nil || r.Detail["trigger"] != "scheduled" {
		t.Fatalf("清理审计 %+v %v", r.ActorEmail, r.Detail)
	}

	// 全组织法律保留：整个组织跳过
	f.c.LegalHold.Create().SetOrgID(f.org).SetReason("全组织").SetCreatedBy(f.u1).ExecX(ctx)
	f.conv(t, f.org, f.u1, old, old, false)
	if res, err := chatretention.Purge(ctx, f.c, w, nil, op, f.now); err != nil || res.Conversations != 0 {
		t.Fatalf("全组织保留不应清理 %+v %v", res, err)
	}
	// 解除后可清理
	f.c.LegalHold.Update().SetReleasedAt(f.now).SetReleasedBy(f.u1).ExecX(ctx)
	if res, err := chatretention.Purge(ctx, f.c, w, nil, op, f.now); err != nil || res.Conversations != 2 {
		t.Fatalf("解除后应清理 2 个（含原按用户保留的）%+v %v", res, err)
	}
}

func existsConv(c *ent.Client, id int) bool {
	_, err := c.Conversation.Get(context.Background(), id)
	return err == nil
}
