package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/api"
	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/model"
	"github.com/SSStq1314/hengdesk/server/internal/ent/usagerecord"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/gateway/mockupstream"
)

func fastRetry(t *testing.T) {
	old := gateway.Retry
	gateway.Retry.Backoff = time.Millisecond
	t.Cleanup(func() { gateway.Retry = old })
}

func attempts(t *testing.T, g *gwEnv) []*ent.UsageRecord {
	t.Helper()
	return g.c.UsageRecord.Query().Order(ent.Asc(usagerecord.FieldID)).AllX(context.Background())
}

func auditAttempts(g *gwEnv) int {
	return g.c.AuditLog.Query().Where(auditlog.Action("model.call"), auditlog.PhaseEQ(auditlog.PhaseAttempt)).CountX(context.Background())
}

// 首字节前 503：同一上游重试一次后成功；非流式与流式都适用。每次尝试都有审计与 usage_record。
func TestChatRetryBeforeFirstByte(t *testing.T) {
	fastRetry(t)
	for i, stream := range []string{"false", "true"} {
		t.Run("stream="+stream, func(t *testing.T) {
			g := newGW(t, false)
			g.m.FailStatus, g.m.FailTimes = 503, 1
			rec := g.admin.do(http.MethodPost, "/api/v1/chat/completions", `{"model":"ds-flash","stream":`+stream+`,"messages":[{"role":"user","content":"hi"}]}`, true)
			if rec.Code != 200 {
				t.Fatalf("重试后应成功：%d %s", rec.Code, rec.Body)
			}
			if i == 1 && !strings.Contains(rec.Body.String(), "event: done") {
				t.Fatalf("sse: %s", rec.Body)
			}
			rs := attempts(t, g)
			if len(rs) != 2 || rs[0].Attempt != 1 || rs[0].Status != usagerecord.StatusError || rs[0].TotalTokens != 0 ||
				rs[1].Attempt != 2 || rs[1].Status != usagerecord.StatusOk || rs[0].RequestID != rs[1].RequestID {
				t.Fatalf("usage_records: %+v", rs)
			}
			if n := auditAttempts(g); n != 2 {
				t.Fatalf("审计 attempt 应 2 条，实际 %d", n)
			}
		})
	}
}

// 主上游持续 503：重试用尽后切换到备用模型；预算只计实际产生的用量（失败尝试 0 token、0 金额）。
func TestChatFailoverToBackupModel(t *testing.T) {
	fastRetry(t)
	g := newGW(t, false)
	ctx := context.Background()
	backup := mockupstream.New(upKey)
	t.Cleanup(backup.Close)
	rec := g.admin.do(http.MethodPost, "/api/v1/admin/model-providers",
		fmt.Sprintf(`{"name":"backup","dialect":"deepseek","base_url":%q,"api_key":%q}`, backup.URL, upKey), true)
	if rec.Code != 201 {
		t.Fatalf("provider %d %s", rec.Code, rec.Body)
	}
	if got := g.admin.do(http.MethodPost, "/api/v1/admin/model-providers", `{"name":"x","kind":"bogus","base_url":"https://a.example"}`, true).Code; got != 400 {
		t.Fatalf("invalid kind %d", got)
	}
	if rec := g.admin.do(http.MethodPost, "/api/v1/admin/model-providers", `{"name":"claude","kind":"anthropic","base_url":"https://api.anthropic.com"}`, true); rec.Code != 201 || !strings.Contains(rec.Body.String(), `"kind":"anthropic"`) {
		t.Fatalf("anthropic provider %d %s", rec.Code, rec.Body)
	}
	var pv struct{ ID int }
	_ = json.Unmarshal(rec.Body.Bytes(), &pv)
	rec = g.admin.do(http.MethodPost, "/api/v1/admin/models",
		fmt.Sprintf(`{"name":"ds-backup","provider_id":%d,"upstream_model":"deepseek-flash","thinking":"off","price_input_micros":1000000,"price_output_micros":2000000}`, pv.ID), true)
	if rec.Code != 201 {
		t.Fatalf("model %d %s", rec.Code, rec.Body)
	}
	var bm struct{ ID int }
	_ = json.Unmarshal(rec.Body.Bytes(), &bm)
	primary := g.c.Model.Query().Where(model.Name("ds-flash")).OnlyIDX(ctx)
	// 校验：不能把自身设为备用；不存在的模型 400
	if got := g.admin.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/models/%d", primary), fmt.Sprintf(`{"fallback_model_ids":[%d]}`, primary), true).Code; got != 400 {
		t.Fatalf("self fallback %d", got)
	}
	if got := g.admin.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/models/%d", primary), `{"fallback_model_ids":[99999]}`, true).Code; got != 400 {
		t.Fatalf("missing fallback %d", got)
	}
	rec = g.admin.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/models/%d", primary),
		fmt.Sprintf(`{"fallback_model_ids":[%d],"price_input_micros":1000000,"price_output_micros":2000000}`, bm.ID), true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), fmt.Sprintf(`"fallback_model_ids":[%d]`, bm.ID)) {
		t.Fatalf("set fallback %d %s", rec.Code, rec.Body)
	}
	g.m.FailStatus = 503
	rec = g.admin.do(http.MethodPost, "/api/v1/chat/completions", `{"model":"ds-flash","messages":[{"role":"user","content":"hi"}]}`, true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"model":"ds-flash"`) {
		t.Fatalf("应切换到备用模型并成功：%d %s", rec.Code, rec.Body)
	}
	if g.m.Count() != gateway.Retry.PerTarget || backup.Count() != 1 {
		t.Fatalf("主上游请求 %d 次、备用 %d 次", g.m.Count(), backup.Count())
	}
	rs := attempts(t, g)
	if len(rs) != 3 || rs[2].ModelID != bm.ID || rs[2].Attempt != 3 || rs[2].Status != usagerecord.StatusOk {
		t.Fatalf("usage_records: %+v", rs)
	}
	var total, tokens int64
	for _, r := range rs[:2] {
		if r.CostMicros != 0 || r.TotalTokens != 0 || r.ModelID != primary {
			t.Fatalf("失败尝试不应计费：%+v", r)
		}
	}
	for _, r := range rs {
		total += r.CostMicros
		tokens += int64(r.TotalTokens)
	}
	if total != rs[2].CostMicros || tokens != int64(rs[2].TotalTokens) || total == 0 {
		t.Fatalf("预算应只计一次实际用量：total=%d last=%d", total, rs[2].CostMicros)
	}
	var fb int
	for _, a := range g.c.AuditLog.Query().Where(auditlog.Action("model.call"), auditlog.PhaseEQ(auditlog.PhaseAttempt)).AllX(ctx) {
		if a.Detail["fallback_model"] == "ds-backup" {
			fb++
		}
	}
	if fb != 1 {
		t.Fatalf("备用模型尝试的审计 %d", fb)
	}
	// 400 类错误不重试、不切换
	g.m.FailStatus = 400
	before, bb := g.m.Count(), backup.Count()
	if rec := g.admin.do(http.MethodPost, "/api/v1/chat/completions", `{"model":"ds-flash","messages":[{"role":"user","content":"hi"}]}`, true); rec.Code != 400 {
		t.Fatalf("400 %d", rec.Code)
	}
	if g.m.Count() != before+1 || backup.Count() != bb {
		t.Fatal("4xx 不应重试或切换")
	}
}

// 重试用尽仍失败：返回最后一次的错误，同一上游请求 PerTarget 次。
func TestChatRetryExhausted(t *testing.T) {
	fastRetry(t)
	g := newGW(t, false)
	g.m.FailStatus = 503
	rec := g.admin.do(http.MethodPost, "/api/v1/chat/completions", `{"model":"ds-flash","messages":[{"role":"user","content":"hi"}]}`, true)
	if rec.Code != 502 || g.m.Count() != gateway.Retry.PerTarget {
		t.Fatalf("%d count=%d", rec.Code, g.m.Count())
	}
}

// failSecondAttempt 第二次 model.call attempt 审计写失败。
type failSecondAttempt struct {
	audit.Writer
	n *int
}

func (w failSecondAttempt) Write(ctx context.Context, e audit.Entry) error {
	if e.Action == "model.call" && e.Phase == "attempt" {
		*w.n++
		if *w.n == 2 {
			return errors.New("audit store down")
		}
	}
	return w.Writer.Write(ctx, e)
}

// 第二次尝试的审计写失败：不再调用上游（fail-closed），返回第一次的错误。
func TestChatRetryStopsWhenAuditFails(t *testing.T) {
	fastRetry(t)
	n := 0
	g := newGW(t, false, func(d *api.Deps) { d.Audit = failSecondAttempt{d.Audit, &n} })
	g.m.FailStatus = 503
	rec := g.admin.do(http.MethodPost, "/api/v1/chat/completions", `{"model":"ds-flash","messages":[{"role":"user","content":"hi"}]}`, true)
	if rec.Code != 502 || g.m.Count() != 1 {
		t.Fatalf("%d count=%d", rec.Code, g.m.Count())
	}
}
