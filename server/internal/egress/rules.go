// Package egress 统一出站 HTTP 客户端（FR-27 / AC-09）。
//
// 所有对外 HTTP 请求（模型上游、OIDC，以及以后的 MCP、连接器）都必须使用本包的 Client：
//   - 白名单为空即全部拒绝；
//   - 在建立连接时解析 DNS，并对解析出的每个 IP 再检查一次（防 DNS rebinding），直接连接已检查的 IP；
//   - 重定向的每一跳重新检查；
//   - 内网、环回、链路本地与云元数据地址默认禁止，只能用 ip / cidr 条目显式放行；
//   - 每一跳写出站日志（不含请求体、请求头、查询串），被拒绝的出站写审计（写失败同样拒绝）。
package egress

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Rule 一条白名单条目。
type Rule struct {
	Kind   string // host / wildcard / ip / cidr
	Value  string
	Ports  []int // 空表示任意端口
	prefix netip.Prefix
}

// metadata 云元数据地址：即使在放行的网段内，也只能用精确的 ip 条目放行。
var metadata = map[netip.Addr]bool{
	netip.MustParseAddr("169.254.169.254"): true, // AWS / GCP / Azure / OpenStack
	netip.MustParseAddr("169.254.170.2"):   true, // AWS ECS
	netip.MustParseAddr("100.100.100.200"): true, // 阿里云
	netip.MustParseAddr("fd00:ec2::254"):   true, // AWS IPv6
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// IsMetadata 是否云元数据地址。
func IsMetadata(a netip.Addr) bool { return metadata[a.Unmap()] }

// IsInternal 内网 / 环回 / 链路本地 / 共享地址 / 元数据等非公网地址。
func IsInternal(a netip.Addr) bool {
	a = a.Unmap()
	return !a.IsGlobalUnicast() || a.IsPrivate() || cgnat.Contains(a) || IsMetadata(a)
}

// ParseRule 校验并规范化一条白名单。ports 为逗号分隔的端口列表。
func ParseRule(kind, value, ports string) (Rule, error) {
	r := Rule{Kind: kind, Value: strings.ToLower(strings.TrimSpace(value))}
	v := r.Value
	switch kind {
	case "host":
		if !validHost(v) {
			return r, fmt.Errorf("域名格式不正确：%q", value)
		}
	case "wildcard":
		if !strings.HasPrefix(v, "*.") || !validHost(v[2:]) || !strings.Contains(v[2:], ".") {
			return r, fmt.Errorf("通配符格式应为 *.example.com：%q", value)
		}
	case "ip":
		a, err := netip.ParseAddr(v)
		if err != nil {
			return r, fmt.Errorf("IP 格式不正确：%q", value)
		}
		a = a.Unmap()
		r.Value, r.prefix = a.String(), netip.PrefixFrom(a, a.BitLen())
	case "cidr":
		p, err := netip.ParsePrefix(v)
		if err != nil {
			return r, fmt.Errorf("CIDR 格式不正确：%q", value)
		}
		p = p.Masked()
		// 0.0.0.0/0、::/0 这类过宽网段等于关闭出站控制，不允许。
		if (p.Addr().Is4() && p.Bits() < 8) || (p.Addr().Is6() && p.Bits() < 16) {
			return r, fmt.Errorf("网段过宽（IPv4 至少 /8，IPv6 至少 /16）：%q", value)
		}
		r.Value, r.prefix = p.String(), p
	default:
		return r, fmt.Errorf("未知类型：%q", kind)
	}
	for _, s := range strings.Split(ports, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 65535 {
			return r, fmt.Errorf("端口不正确：%q", s)
		}
		r.Ports = append(r.Ports, n)
	}
	return r, nil
}

// PortsString 规范化后的端口串。
func (r Rule) PortsString() string {
	s := make([]string, len(r.Ports))
	for i, p := range r.Ports {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, ",")
}

// CoversInternal 该条目是否放行了内网地址（用于审计标记）。
func (r Rule) CoversInternal() bool {
	if r.Kind != "ip" && r.Kind != "cidr" {
		return false
	}
	p := r.prefix
	if IsInternal(p.Addr()) {
		return true
	}
	for _, q := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16", "100.64.0.0/10", "fc00::/7", "fe80::/10", "::1/128"} {
		if netip.MustParsePrefix(q).Overlaps(p) {
			return true
		}
	}
	return false
}

func validHost(h string) bool {
	if h == "" || len(h) > 253 || strings.HasSuffix(h, ".") {
		return false
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return false // IP 请用 ip 类型
	}
	for _, l := range strings.Split(h, ".") {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, c := range l {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
				return false
			}
		}
	}
	return true
}

func (r Rule) portOK(port int) bool {
	if len(r.Ports) == 0 {
		return true
	}
	for _, p := range r.Ports {
		if p == port {
			return true
		}
	}
	return false
}

func (r Rule) hostMatch(host string) bool {
	switch r.Kind {
	case "host":
		return host == r.Value
	case "wildcard":
		return strings.HasSuffix(host, r.Value[1:])
	}
	return false
}

// 拒绝原因。
const (
	ReasonEmpty       = "allowlist_empty"    // 白名单为空
	ReasonNotAllowed  = "not_in_allowlist"   // 不在白名单
	ReasonPort        = "port_not_allowed"   // 主机在白名单但端口不允许
	ReasonInternal    = "internal_address"   // 内网 / 环回 / 链路本地，未显式放行
	ReasonMetadata    = "metadata_address"   // 云元数据地址
	ReasonInvalid     = "invalid_address"    // 未指定 / 组播等不可用地址
	ReasonScheme      = "scheme_not_allowed" // 非 http / https
	ReasonDNS         = "dns_failed"         // 解析失败
	ReasonRedirects   = "too_many_redirects" // 重定向次数过多
	ReasonAuditFailed = "audit_unavailable"  // 审计不可用（fail-closed）
	ReasonPolicyError = "policy_unavailable" // 白名单读取失败（fail-closed）
)

// HostAllowed 只按主机名与端口判断（解析前的快速检查，以及「新建上游是否已在白名单」的提示）。
// IP 直连主机按 IP 规则判断。
func HostAllowed(rules []Rule, host string, port int) (bool, string) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if len(rules) == 0 {
		return false, ReasonEmpty
	}
	if a, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return Decide(rules, host, port, a)
	}
	reason := ReasonNotAllowed
	for _, r := range rules {
		if r.hostMatch(host) {
			if r.portOK(port) {
				return true, ""
			}
			reason = ReasonPort
		}
		if r.Kind == "ip" || r.Kind == "cidr" {
			// 有 IP 规则时需解析后才能判断，先放过到连接阶段。
			if r.portOK(port) {
				return true, ""
			}
		}
	}
	return false, reason
}

// Decide 判断访问 host:port 解析出的地址 a 是否允许。
func Decide(rules []Rule, host string, port int, a netip.Addr) (bool, string) {
	a = a.Unmap()
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if len(rules) == 0 {
		return false, ReasonEmpty
	}
	if !a.IsValid() || a.IsUnspecified() || a.IsMulticast() || a.IsInterfaceLocalMulticast() {
		return false, ReasonInvalid
	}
	ipRule := func(exact bool) (bool, string) {
		reason := ""
		for _, r := range rules {
			if (r.Kind == "ip" || r.Kind == "cidr") && r.prefix.Contains(a) && (!exact || r.prefix.Bits() == a.BitLen()) {
				if r.portOK(port) {
					return true, ""
				}
				reason = ReasonPort
			}
		}
		return false, reason
	}
	switch {
	case IsMetadata(a):
		if ok, why := ipRule(true); ok || why != "" {
			return ok, why
		}
		return false, ReasonMetadata
	case IsInternal(a):
		if ok, why := ipRule(false); ok || why != "" {
			return ok, why
		}
		return false, ReasonInternal
	}
	if ok, _ := ipRule(false); ok {
		return true, ""
	}
	reason := ReasonNotAllowed
	for _, r := range rules {
		if r.hostMatch(host) {
			if r.portOK(port) {
				return true, ""
			}
			reason = ReasonPort
		}
	}
	return false, reason
}

// ReasonText 拒绝原因的中文说明。
func ReasonText(reason string) string {
	switch reason {
	case ReasonEmpty:
		return "出站白名单为空，所有出站均被拒绝"
	case ReasonNotAllowed:
		return "目标不在出站白名单中"
	case ReasonPort:
		return "目标端口不在白名单允许范围内"
	case ReasonInternal:
		return "目标是内网或环回地址，需管理员用 IP / 网段条目显式放行"
	case ReasonMetadata:
		return "目标是云元数据地址，默认禁止"
	case ReasonInvalid:
		return "目标地址不可用"
	case ReasonScheme:
		return "只允许 http / https"
	case ReasonDNS:
		return "域名解析失败"
	case ReasonRedirects:
		return "重定向次数过多"
	case ReasonAuditFailed:
		return "审计服务不可用，出站已拒绝"
	case ReasonPolicyError:
		return "出站白名单暂不可读，出站已拒绝"
	}
	return reason
}
