package mcp

import (
	"context"
	"fmt"

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/tool"
)

// ToolKey MCP 工具在目录中的唯一键（ACL 的 resource_id 也用它）。
func ToolKey(serverID int, name string) string { return fmt.Sprintf("mcp:%d:%s", serverID, name) }

// SyncStats 一次发现同步的结果。
type SyncStats struct {
	Added     []string `json:"added"`
	Changed   []string `json:"changed"`
	Removed   []string `json:"removed"`
	Restored  []string `json:"restored"`
	Unchanged int      `json:"unchanged"`
}

// Sync 把发现结果写入工具目录（PRD §9.6）：
//   - 新工具：默认禁用、默认需审批，风险等级取建议值等待管理员确认；
//   - 名称 / 描述 / 入参 schema 变化：更新内容，标记待复核并自动禁用；
//   - 服务端已不再提供：标记已下线并禁用（保留记录与授权，便于审计与恢复）；
//   - 下线后重新出现：取消下线，但保持禁用并标记待复核。
//
// 必须在事务中调用。
func Sync(ctx context.Context, tx *ent.Tx, orgID, serverID int, found []Tool) (SyncStats, error) {
	var st SyncStats
	existing, err := tx.Tool.Query().Where(tool.ServerID(serverID)).All(ctx)
	if err != nil {
		return st, err
	}
	byKey := map[string]*ent.Tool{}
	for _, t := range existing {
		byKey[t.Key] = t
	}
	seen := map[string]bool{}
	for _, f := range found {
		key := ToolKey(serverID, f.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		cur := byKey[key]
		if cur == nil {
			err := tx.Tool.Create().SetKey(key).SetName(f.Name).SetSource("mcp").SetOrgID(orgID).SetServerID(serverID).
				SetDescription(f.Description).SetInputSchema(f.InputSchema).SetAnnotations(f.Annotations).
				SetSchemaHash(f.Hash).SetSuggestedRisk(tool.SuggestedRisk(f.SuggestedRisk)).SetRiskLevel(tool.RiskLevel(f.SuggestedRisk)).
				SetEnabled(false).SetExecMode(tool.ExecModeRequireApproval).Exec(ctx)
			if err != nil {
				return st, err
			}
			st.Added = append(st.Added, f.Name)
			continue
		}
		u := cur.Update().SetAnnotations(f.Annotations).SetSuggestedRisk(tool.SuggestedRisk(f.SuggestedRisk))
		switch {
		case cur.Removed:
			u.SetRemoved(false).SetNeedsReview(true).SetEnabled(false).
				SetDescription(f.Description).SetInputSchema(f.InputSchema).SetSchemaHash(f.Hash)
			st.Restored = append(st.Restored, f.Name)
		case cur.SchemaHash != f.Hash:
			u.SetNeedsReview(true).SetEnabled(false).
				SetDescription(f.Description).SetInputSchema(f.InputSchema).SetSchemaHash(f.Hash)
			st.Changed = append(st.Changed, f.Name)
		default:
			st.Unchanged++
		}
		if err := u.Exec(ctx); err != nil {
			return st, err
		}
	}
	for _, t := range existing {
		if !seen[t.Key] && !t.Removed {
			if err := t.Update().SetRemoved(true).SetEnabled(false).Exec(ctx); err != nil {
				return st, err
			}
			st.Removed = append(st.Removed, t.Name)
		}
	}
	return st, nil
}
