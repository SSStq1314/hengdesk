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
