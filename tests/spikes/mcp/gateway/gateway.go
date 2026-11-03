// Package gateway 演示在 mcp-go 客户端调用前插入策略/审批钩子与审计（fail-closed）。
package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

type Decision int

const (
	Deny Decision = iota // 零值即拒绝：fail-closed
	Allow
	NeedApproval
)

type Policy interface {
	Decide(ctx context.Context, actor, tool string, args map[string]any) (Decision, error)
}

type Approver interface {
	// 阻塞等待审批结果；返回 false 表示拒绝；ctx 超时表示审批超时。
	Await(ctx context.Context, actor, tool string, args map[string]any) (bool, error)
}

type AuditEvent struct {
	TraceID, Actor, Tool, Stage, Result string
	ArgsSHA256                          string
	At                                  time.Time
}

type Auditor interface {
	Write(ctx context.Context, e AuditEvent) error
}

var (
	ErrDenied        = errors.New("policy denied")
	ErrRejected      = errors.New("approval rejected")
	ErrApprovalTO    = errors.New("approval timeout")
	ErrAuditFailed   = errors.New("audit unavailable, fail-closed")
	ErrPolicyFailed  = errors.New("policy engine error, fail-closed")
)

type Gateway struct {
	C               *client.Client
	Policy          Policy
	Approver        Approver
	Audit           Auditor
	CallTimeout     time.Duration
	ApprovalTimeout time.Duration
}

func hashArgs(a map[string]any) string {
	b, _ := json.Marshal(a)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

// CallTool：策略 → (审批) → 审计「准备执行」成功 → 调用 → 审计结果。任何一环出错都不执行工具。
func (g *Gateway) CallTool(ctx context.Context, trace, actor, tool string, args map[string]any) (*mcp.CallToolResult, error) {
	ev := AuditEvent{TraceID: trace, Actor: actor, Tool: tool, ArgsSHA256: hashArgs(args)}
	audit := func(stage, result string) error {
		ev.Stage, ev.Result, ev.At = stage, result, time.Now()
		if g.Audit == nil {
			return ErrAuditFailed
		}
		if err := g.Audit.Write(ctx, ev); err != nil {
			return fmt.Errorf("%w: %v", ErrAuditFailed, err)
		}
		return nil
	}
	if g.Policy == nil {
		_ = audit("policy", "deny:no-policy")
		return nil, ErrPolicyFailed
	}
	d, err := g.Policy.Decide(ctx, actor, tool, args)
	if err != nil {
		_ = audit("policy", "error")
		return nil, fmt.Errorf("%w: %v", ErrPolicyFailed, err)
	}
	switch d {
	case Allow:
	case NeedApproval:
		if g.Approver == nil {
			_ = audit("approval", "deny:no-approver")
			return nil, ErrDenied
		}
		actx, cancel := context.WithTimeout(ctx, g.ApprovalTimeout)
		ok, err := g.Approver.Await(actx, actor, tool, args)
		cancel()
		if errors.Is(err, context.DeadlineExceeded) {
			_ = audit("approval", "timeout")
			return nil, ErrApprovalTO
		}
		if err != nil || !ok {
			_ = audit("approval", "rejected")
			return nil, ErrRejected
		}
	default:
		_ = audit("policy", "deny")
		return nil, ErrDenied
	}
	if err := audit("pre-exec", "allowed"); err != nil { // 审计写不进去就不执行
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, g.CallTimeout)
	defer cancel()
	req := mcp.CallToolRequest{}
	req.Params.Name = tool
	req.Params.Arguments = args
	res, err := g.C.CallTool(cctx, req)
	switch {
	case err != nil:
		_ = audit("exec", "transport-error:"+err.Error())
	case res.IsError:
		_ = audit("exec", "tool-error")
	default:
		_ = audit("exec", "ok")
	}
	return res, err
}
