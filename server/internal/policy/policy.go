// Package policy 工具治理的统一判定（Policy Engine，PRD §9.7）。
//
// 是否需要审批只看平台策略 exec_mode，不看 MCP 工具自带注解（D-27）。
// 所有入口（工具列表、模型可见工具、实际执行前）都必须经过 Decide，不得自行拼条件。
package policy

import (
	"github.com/SSStq1314/hengdesk/server/internal/ent"
)

// 执行方式。
const (
	ModeAuto     = "auto"
	ModeApproval = "require_approval"
	ModeDeny     = "deny"
)

// 风险等级。
const (
	RiskReadOnly = "read_only"
	RiskWrite    = "write"
	RiskHigh     = "high_risk"
)

// 不可用原因（仅用于审计与管理端提示，不向员工展示具体原因）。
const (
	ReasonOK           = ""
	ReasonDisabled     = "tool_disabled"
	ReasonDenied       = "exec_denied"
	ReasonNeedsReview  = "needs_review"
	ReasonRemoved      = "tool_removed"
	ReasonServerOff    = "server_disabled"
	ReasonServerAbsent = "server_missing"
	ReasonOtherOrg     = "other_org"
)

// Decision 工具对某组织的可用性与执行方式（不含个人 ACL，ACL 由 authz.ToolUsable 叠加）。
type Decision struct {
	Usable bool
	Mode   string // auto / require_approval（Usable=false 时无意义）
	Reason string
}

// Decide 判定工具当前能否被调用、以什么方式调用。server 为 MCP 工具所属服务器（非 MCP 工具传 nil）。
func Decide(orgID int, t *ent.Tool, server *ent.McpServer) Decision {
	switch {
	case t == nil:
		return Decision{Reason: ReasonDisabled}
	case t.OrgID != orgID:
		return Decision{Reason: ReasonOtherOrg}
	case t.Removed:
		return Decision{Reason: ReasonRemoved}
	case t.NeedsReview:
		return Decision{Reason: ReasonNeedsReview}
	case !t.Enabled:
		return Decision{Reason: ReasonDisabled}
	case string(t.ExecMode) == ModeDeny:
		return Decision{Reason: ReasonDenied}
	}
	if t.ServerID != nil {
		if server == nil || server.ID != *t.ServerID {
			return Decision{Reason: ReasonServerAbsent}
		}
		if !server.Enabled || server.OrgID != orgID {
			return Decision{Reason: ReasonServerOff}
		}
	}
	mode := string(t.ExecMode)
	// 兜底：高危工具即使库里被写成 auto，也按需审批处理。
	if t.RiskLevel == RiskHigh && mode == ModeAuto {
		mode = ModeApproval
	}
	return Decision{Usable: true, Mode: mode}
}

// ValidateSettings 校验风险等级与执行方式的组合：高危工具不能自动执行。
func ValidateSettings(risk, mode string) string {
	switch risk {
	case RiskReadOnly, RiskWrite, RiskHigh:
	default:
		return "风险等级只能是 read_only / write / high_risk"
	}
	switch mode {
	case ModeAuto, ModeApproval, ModeDeny:
	default:
		return "执行策略只能是 auto / require_approval / deny"
	}
	if risk == RiskHigh && mode == ModeAuto {
		return "高危工具不能设为自动执行"
	}
	return ""
}
