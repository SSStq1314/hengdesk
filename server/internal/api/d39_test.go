package api_test

// D-39 第 6、7 条：备用模型只切换到调用人有使用权限的；配置界面提示使用人群不一致。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/ent/model"
	"github.com/SSStq1314/hengdesk/server/internal/ent/usagerecord"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/gateway/mockupstream"
)

// addBackupModel 新建一个指向独立 mock 上游的备用模型 ds-backup，并设为 ds-flash 的备用。返回备用上游与两个模型 ID。
func addBackupModel(t *testing.T, g *gwEnv) (*mockupstream.Server, int, int) {
	t.Helper()
	ctx := context.Background()
	backup := mockupstream.New(upKey)
	t.Cleanup(backup.Close)
	rec := g.admin.do(http.MethodPost, "/api/v1/admin/model-providers",
		fmt.Sprintf(`{"name":"backup","dialect":"deepseek","base_url":%q,"api_key":%q}`, backup.URL, upKey), true)
	if rec.Code != 201 {
		t.Fatalf("provider %d %s", rec.Code, rec.Body)
	}
	var pv struct{ ID int }
	_ = json.Unmarshal(rec.Body.Bytes(), &pv)
	rec = g.admin.do(http.MethodPost, "/api/v1/admin/models",
		fmt.Sprintf(`{"name":"ds-backup","provider_id":%d,"upstream_model":"deepseek-flash","thinking":"off"}`, pv.ID), true)
	if rec.Code != 201 {
		t.Fatalf("model %d %s", rec.Code, rec.Body)
	}
	var bm struct{ ID int }
	_ = json.Unmarshal(rec.Body.Bytes(), &bm)
	primary := g.c.Model.Query().Where(model.Name("ds-flash")).OnlyIDX(ctx)
	if rec := g.admin.do(http.MethodPatch, fmt.Sprintf("/api/v1/admin/models/%d", primary), fmt.Sprintf(`{"fallback_model_ids":[%d]}`, bm.ID), true); rec.Code != 200 {
		t.Fatalf("set fallback %d %s", rec.Code, rec.Body)
	}
	return backup, primary, bm.ID
}

func grantUse(t *testing.T, g *gwEnv, modelID, userID int) {
	t.Helper()
	if rec := g.admin.do(http.MethodPost, fmt.Sprintf("/api/v1/admin/models/%d/acl", modelID), fmt.Sprintf(`{"subject_type":"user","subject_id":%d}`, userID), true); rec.Code != 204 {
		t.Fatalf("grant %d %s", rec.Code, rec.Body)
	}
}

func TestFallbackRequiresUsePermission(t *testing.T) {
	fastRetry(t)
	for _, stream := range []string{"false", "true"} {
		t.Run("stream="+stream, func(t *testing.T) {
			g := newGW(t, false)
			backup, primary, bid := addBackupModel(t, g)
			memID := atoi(g.uid(t, "member@x.local"))
			grantUse(t, g, primary, memID) // 只有主模型的使用权限
			mem := g.loginAs(t, "member@x.local", "password123")
			g.m.FailStatus = 503
			body := `{"model":"ds-flash","stream":` + stream + `,"messages":[{"role":"user","content":"hi"}]}`
			rec := mem.do(http.MethodPost, "/api/v1/chat/completions", body, true)
			if rec.Code != 502 || !strings.Contains(rec.Body.String(), "upstream") {
				t.Fatalf("无备用模型权限时应返回主模型原错误：%d %s", rec.Code, rec.Body)
			}
			if backup.Count() != 0 || g.m.Count() != gateway.Retry.PerTarget {
				t.Fatalf("不应调用无权限的备用模型：backup=%d primary=%d", backup.Count(), g.m.Count())
			}
			if n := g.c.UsageRecord.Query().Where(usagerecord.ModelID(bid)).CountX(context.Background()); n != 0 {
				t.Fatalf("备用模型不应有用量记录 %d", n)
			}
			// 授予备用模型的使用权限后可以切换
			grantUse(t, g, bid, memID)
			rec = mem.do(http.MethodPost, "/api/v1/chat/completions", body, true)
			if rec.Code != 200 || backup.Count() != 1 || !strings.Contains(rec.Body.String(), `"fallback_from":"ds-flash"`) {
				t.Fatalf("有权限后应切换：%d backup=%d %s", rec.Code, backup.Count(), rec.Body)
			}
		})
	}
}

func TestFallbackACLMismatchHint(t *testing.T) {
	g := newGW(t, false)
	_, primary, bid := addBackupModel(t, g)
	memID := atoi(g.uid(t, "member@x.local"))
	mismatch := func() []int {
		rec := g.admin.do(http.MethodGet, "/api/v1/admin/models", "", false)
		var out struct {
			Items []struct {
				ID       int   `json:"id"`
				Mismatch []int `json:"fallback_acl_mismatch"`
			}
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		for _, it := range out.Items {
			if it.ID == primary {
				return it.Mismatch
			}
		}
		t.Fatal("primary not listed")
		return nil
	}
	if m := mismatch(); len(m) != 0 {
		t.Fatalf("都没有授权时人群一致：%v", m)
	}
	grantUse(t, g, primary, memID)
	if m := mismatch(); len(m) != 1 || m[0] != bid {
		t.Fatalf("人群不一致应提示：%v", m)
	}
	grantUse(t, g, bid, memID)
	if m := mismatch(); len(m) != 0 {
		t.Fatalf("授权一致后不提示：%v", m)
	}
}

// D-39 第 8 条：流式调用收到首个 token 后上游失败，不重试、不切换，直接返回错误（SSE error 事件）。
func TestStreamNoSwitchAfterFirstToken(t *testing.T) {
	fastRetry(t)
	g := newGW(t, false)
	backup, _, _ := addBackupModel(t, g)
	g.m.CutAfter = 2
	rec := g.admin.do(http.MethodPost, "/api/v1/chat/completions", `{"model":"ds-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`, true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "event: error") || strings.Contains(rec.Body.String(), "event: done") {
		t.Fatalf("首 token 后失败应返回 error 事件：%d %s", rec.Code, rec.Body)
	}
	if g.m.Count() != 1 || backup.Count() != 0 {
		t.Fatalf("首 token 后不应重试或切换：primary=%d backup=%d", g.m.Count(), backup.Count())
	}
}
