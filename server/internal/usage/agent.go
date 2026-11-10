package usage

import "context"

type agentIDCtxKey struct{}

// WithAgentID 把本轮调用所属的智能体 ID 写入 context，供 usage / embedding 记账（FR-13）。
func WithAgentID(ctx context.Context, id int) context.Context {
	if id <= 0 {
		return ctx
	}
	return context.WithValue(ctx, agentIDCtxKey{}, id)
}

// AgentIDFrom 读取 context 中的智能体 ID；没有则返回 nil。
func AgentIDFrom(ctx context.Context) *int {
	v, ok := ctx.Value(agentIDCtxKey{}).(int)
	if !ok || v <= 0 {
		return nil
	}
	id := v
	return &id
}
