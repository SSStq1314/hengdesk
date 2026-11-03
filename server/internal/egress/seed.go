package egress

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
	"github.com/SSStq1314/hengdesk/server/internal/ent/egressrule"
	"github.com/SSStq1314/hengdesk/server/internal/ent/systemsetting"
)

// SeedSettingKey 升级迁移只执行一次的标记。
const SeedSettingKey = "egress.upgrade_seed"

// SeedRule 升级时写入的一条规则。
type SeedRule struct {
	Kind, Value, Ports, Source, Note string
	Private                          bool
}

// PlanSeed 根据已有上游 base_url（和 OIDC issuer）算出需要写入的白名单。
// 域名写 host 条目；若当前解析为内网地址，另写对应 IP 条目（否则升级后立即不可用）。
// 云元数据地址永不写入。
func PlanSeed(ctx context.Context, res Resolver, urls map[string]string) []SeedRule {
	seen := map[string]bool{}
	var out []SeedRule
	add := func(r SeedRule) {
		k := r.Kind + "|" + r.Value + "|" + r.Ports
		if !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	for raw, source := range urls {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			continue
		}
		host := strings.ToLower(u.Hostname())
		port := strconv.Itoa(portOf(u))
		note := "升级自动写入：" + source
		if a, err := netip.ParseAddr(host); err == nil {
			if IsMetadata(a) {
				continue
			}
			add(SeedRule{Kind: "ip", Value: a.Unmap().String(), Ports: port, Source: srcOf(source), Note: note, Private: IsInternal(a)})
			continue
		}
		add(SeedRule{Kind: "host", Value: host, Ports: port, Source: srcOf(source), Note: note})
		if res == nil {
			res = net.DefaultResolver
		}
		rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		addrs, _ := res.LookupNetIP(rctx, "ip", host)
		cancel()
		for _, a := range addrs {
			if IsInternal(a) && !IsMetadata(a) {
				add(SeedRule{Kind: "ip", Value: a.Unmap().String(), Ports: port, Source: srcOf(source), Note: note + "（" + host + " 解析到的内网地址）", Private: true})
			}
		}
	}
	return out
}

func srcOf(s string) string {
	if strings.HasPrefix(s, "oidc") {
		return "oidc"
	}
	return "upgrade"
}

// Seed 升级迁移：仅执行一次。把已有模型上游（及 OIDC issuer）的主机写入白名单，每条写审计。
// 全新安装（没有上游）也会写标记，之后白名单保持为空 = 全部拒绝。
func Seed(ctx context.Context, db *ent.Client, w audit.Writer, res Resolver, oidcIssuer string) (int, error) {
	if n, err := db.SystemSetting.Query().Where(systemsetting.Key(SeedSettingKey)).Count(ctx); err != nil || n > 0 {
		return 0, err
	}
	urls := map[string]string{}
	ps, err := db.ModelProvider.Query().All(ctx)
	if err != nil {
		return 0, err
	}
	for _, p := range ps {
		urls[p.BaseURL] = "模型上游 " + p.Name
	}
	if oidcIssuer != "" && len(ps) > 0 {
		// 只在升级（已有数据）时写 OIDC；全新安装由管理员在向导里自行添加。
		urls[oidcIssuer] = "oidc issuer"
	}
	plan := PlanSeed(ctx, res, urls)
	tx, err := db.Tx(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range plan {
		exists, err := tx.EgressRule.Query().Where(egressrule.KindEQ(egressrule.Kind(r.Kind)), egressrule.Value(r.Value), egressrule.Ports(r.Ports)).Exist(ctx)
		if err != nil {
			_ = tx.Rollback()
			return 0, err
		}
		if exists {
			continue
		}
		row, err := tx.EgressRule.Create().SetKind(egressrule.Kind(r.Kind)).SetValue(r.Value).SetPorts(r.Ports).
			SetNote(truncate(r.Note, 200)).SetSource(egressrule.Source(r.Source)).SetPrivate(r.Private).Save(ctx)
		if err != nil {
			_ = tx.Rollback()
			return 0, err
		}
		if err := writerFor(w, tx).Write(ctx, audit.Entry{ActorEmail: "system", Action: "egress.rule.seed", ResourceType: "egress_rule",
			ResourceID: strconv.Itoa(row.ID), Phase: "result", Outcome: "success",
			Detail: map[string]any{"kind": r.Kind, "value": r.Value, "ports": r.Ports, "source": r.Source, "private": r.Private, "note": r.Note}}); err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("egress seed audit: %w", err)
		}
		n++
	}
	if err := tx.SystemSetting.Create().SetKey(SeedSettingKey).SetValue(map[string]any{"rules": n, "at": time.Now().UTC().Format(time.RFC3339)}).Exec(ctx); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	return n, tx.Commit()
}

// writerFor 默认审计写入器改为走同一事务（审计与白名单同时生效或同时回滚）。
func writerFor(w audit.Writer, tx *ent.Tx) audit.Writer {
	if _, ok := w.(audit.EntWriter); ok {
		return audit.EntWriter{C: tx.Client()}
	}
	return w
}

// PortsSettingKey D-38 第 3 条端口语义迁移的执行标记。
const PortsSettingKey = "egress.d38_ports"

// MigratePorts D-38 第 3 条：端口留空从「任意端口」改为「仅 443」。升级时执行一次，修正已有的空端口条目：
//   - 升级 / OIDC 自动添加的条目：改为上游 URL 的实际端口（按当前模型上游与 OIDC issuer 重新计算；找不到时为 443）；
//   - 管理员手工添加的条目：改为「*」，保持升级前的放行范围不变，管理端显示任意端口风险提示，由管理员自行收紧。
//
// 每条改动写一条 egress.rule.ports_migrate 审计，与改动同事务（审计写不进去整体回滚）。
func MigratePorts(ctx context.Context, db *ent.Client, w audit.Writer, res Resolver, oidcIssuer string) (int, error) {
	if n, err := db.SystemSetting.Query().Where(systemsetting.Key(PortsSettingKey)).Count(ctx); err != nil || n > 0 {
		return 0, err
	}
	rows, err := db.EgressRule.Query().Where(egressrule.Ports("")).All(ctx)
	if err != nil {
		return 0, err
	}
	actual := map[string]string{} // kind|value -> 实际端口
	if len(rows) > 0 {
		urls := map[string]string{}
		ps, err := db.ModelProvider.Query().All(ctx)
		if err != nil {
			return 0, err
		}
		for _, p := range ps {
			urls[p.BaseURL] = p.Name
		}
		if oidcIssuer != "" {
			urls[oidcIssuer] = "oidc"
		}
		for _, r := range PlanSeed(ctx, res, urls) {
			if _, ok := actual[r.Kind+"|"+r.Value]; !ok {
				actual[r.Kind+"|"+r.Value] = r.Ports
			}
		}
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		to := "*"
		if r.Source != egressrule.SourceManual {
			to = strconv.Itoa(DefaultPort)
			if p := actual[string(r.Kind)+"|"+r.Value]; p != "" {
				to = p
			}
		}
		dup, err := tx.EgressRule.Query().Where(egressrule.KindEQ(r.Kind), egressrule.Value(r.Value), egressrule.Ports(to)).Exist(ctx)
		if err != nil {
			_ = tx.Rollback()
			return 0, err
		}
		if dup {
			continue // 已有同样的条目；空端口条目按新语义只放行 443，范围不会变大
		}
		if err := tx.EgressRule.UpdateOneID(r.ID).SetPorts(to).Exec(ctx); err != nil {
			_ = tx.Rollback()
			return 0, err
		}
		if err := writerFor(w, tx).Write(ctx, audit.Entry{ActorEmail: "system", Action: "egress.rule.ports_migrate", ResourceType: "egress_rule",
			ResourceID: strconv.Itoa(r.ID), Phase: "result", Outcome: "success",
			Detail: map[string]any{"kind": string(r.Kind), "value": r.Value, "source": string(r.Source), "before": "", "after": to, "decision": "D-38-3"}}); err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("egress ports migrate audit: %w", err)
		}
		n++
	}
	if err := tx.SystemSetting.Create().SetKey(PortsSettingKey).SetValue(map[string]any{"rules": n, "at": time.Now().UTC().Format(time.RFC3339)}).Exec(ctx); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	return n, tx.Commit()
}

// D39PortsSettingKey D-39 第 1 条数据修正只执行一次的标记。
const D39PortsSettingKey = "egress.d39_ports"

// MigrateD39Ports D-39 第 1 条：#31（D-38）把空端口的手工条目改成了「*」（任意端口），范围比原来的「仅 443」大。
// 本迁移按 #31 写下的 egress.rule.ports_migrate 审计找到这些条目，收窄为 443（被模型上游 / OIDC 引用的主机用实际端口），
// 并标记「待复核」（review_required）。迁移之后管理员改过的条目（有 egress.rule.update 审计，或端口已不是 *）不动。
// 每改一条写一条审计；审计写失败整体回滚、启动失败（fail-closed）。只执行一次（标记 egress.d39_ports）。
func MigrateD39Ports(ctx context.Context, db *ent.Client, w audit.Writer, res Resolver, oidcIssuer string) (int, error) {
	if n, err := db.SystemSetting.Query().Where(systemsetting.Key(D39PortsSettingKey)).Count(ctx); err != nil || n > 0 {
		return 0, err
	}
	logs, err := db.AuditLog.Query().Where(auditlog.Action("egress.rule.ports_migrate"), auditlog.ResourceType("egress_rule")).
		Order(ent.Asc(auditlog.FieldID)).All(ctx)
	if err != nil {
		return 0, err
	}
	type cand struct {
		rule *ent.EgressRule
		at   time.Time
	}
	var cands []cand
	for _, l := range logs {
		if l.Detail["source"] != string(egressrule.SourceManual) || l.Detail["after"] != "*" || l.Detail["before"] != "" {
			continue
		}
		id, err := strconv.Atoi(l.ResourceID)
		if err != nil {
			continue
		}
		r, err := db.EgressRule.Get(ctx, id)
		if ent.IsNotFound(err) {
			continue // 已删除
		} else if err != nil {
			return 0, err
		}
		if r.Ports != "*" {
			continue
		}
		edited, err := db.AuditLog.Query().Where(auditlog.ResourceType("egress_rule"), auditlog.ResourceID(l.ResourceID),
			auditlog.ActionIn("egress.rule.update", "egress.rule.review"), auditlog.TsGT(l.Ts)).Exist(ctx)
		if err != nil {
			return 0, err
		}
		if edited {
			continue // 管理员在 #31 之后改过或确认过，尊重管理员的选择
		}
		cands = append(cands, cand{r, l.Ts})
	}
	actual := map[string]string{}
	if len(cands) > 0 {
		urls := map[string]string{}
		ps, err := db.ModelProvider.Query().All(ctx)
		if err != nil {
			return 0, err
		}
		for _, p := range ps {
			urls[p.BaseURL] = p.Name
		}
		if oidcIssuer != "" {
			urls[oidcIssuer] = "oidc"
		}
		for _, r := range PlanSeed(ctx, res, urls) {
			if _, ok := actual[r.Kind+"|"+r.Value]; !ok {
				actual[r.Kind+"|"+r.Value] = r.Ports
			}
		}
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return 0, err
	}
	fail := func(err error) (int, error) {
		_ = tx.Rollback()
		return 0, err
	}
	n := 0
	for _, c := range cands {
		r := c.rule
		to := strconv.Itoa(DefaultPort)
		if p := actual[string(r.Kind)+"|"+r.Value]; p != "" {
			to = p
		}
		detail := map[string]any{"kind": string(r.Kind), "value": r.Value, "source": string(r.Source), "before": "*", "after": to,
			"decision": "D-39-1", "review_required": true}
		dup, err := tx.EgressRule.Query().Where(egressrule.KindEQ(r.Kind), egressrule.Value(r.Value), egressrule.Ports(to)).Only(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return fail(err)
		}
		if dup != nil {
			// 已有收窄后的同样条目：删除这条 * 条目，并把已有条目标为待复核。
			if err := tx.EgressRule.DeleteOneID(r.ID).Exec(ctx); err != nil {
				return fail(err)
			}
			if err := tx.EgressRule.UpdateOneID(dup.ID).SetReviewRequired(true).ClearReviewedAt().Exec(ctx); err != nil {
				return fail(err)
			}
			detail["merged_into"] = dup.ID
		} else if err := tx.EgressRule.UpdateOneID(r.ID).SetPorts(to).SetReviewRequired(true).ClearReviewedAt().Exec(ctx); err != nil {
			return fail(err)
		}
		if err := writerFor(w, tx).Write(ctx, audit.Entry{ActorEmail: "system", Action: "egress.rule.ports_migrate", ResourceType: "egress_rule",
			ResourceID: strconv.Itoa(r.ID), Phase: "result", Outcome: "success", Detail: detail}); err != nil {
			return fail(fmt.Errorf("egress d39 ports migrate audit: %w", err))
		}
		n++
	}
	if err := tx.SystemSetting.Create().SetKey(D39PortsSettingKey).SetValue(map[string]any{"rules": n, "at": time.Now().UTC().Format(time.RFC3339)}).Exec(ctx); err != nil {
		return fail(err)
	}
	return n, tx.Commit()
}
