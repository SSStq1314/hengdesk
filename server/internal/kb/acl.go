package kb

import (
	"context"
	"strconv"

	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/authz"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/kbfile"
	"github.com/SSStq1314/hengdesk/server/internal/ent/knowledgebase"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
)

// Access 一个用户对知识库的权限视图（每次请求现算，ACL 改动立即生效）。
//
// 规则（m1-block4.md 待确认 5 / 11）：
//   - 知识库：view 可检索与查看文件列表；edit 可上传 / 删除 / 重试文件；manage 可改设置、ACL、删除知识库。
//   - 文件默认继承知识库 ACL；acl_custom=true 的文件只对「文件 ACL 有 view 的人」与「知识库 manage」可见。
//   - 超级管理员没有隐式访问权。
type Access struct {
	KB    map[int]int  // kb_id → 规范化权限位
	Files map[int]bool // acl_custom 文件中单独授予了 view 的 file_id
}

// LoadAccess 计算 p 的知识库权限。
func LoadAccess(ctx context.Context, az *authz.Service, p *auth.Principal) (*Access, error) {
	a := &Access{KB: map[int]int{}, Files: map[int]bool{}}
	for _, bit := range []int{rbac.BitView, rbac.BitUse, rbac.BitEdit, rbac.BitManage} {
		ids, err := az.VisibleIDs(ctx, p, rbac.ResKB, bit)
		if err != nil {
			return nil, err
		}
		for _, s := range ids {
			if id, err := strconv.Atoi(s); err == nil {
				a.KB[id] |= bit
			}
		}
	}
	fids, err := az.VisibleIDs(ctx, p, rbac.ResKBFile, rbac.BitView)
	if err != nil {
		return nil, err
	}
	for _, s := range fids {
		if id, err := strconv.Atoi(s); err == nil {
			a.Files[id] = true
		}
	}
	return a, nil
}

// Has 是否拥有知识库的 bit。
func (a *Access) Has(kbID, bit int) bool { return a.KB[kbID]&bit != 0 }

// CanSeeFile 是否可见该文件（不含状态判断）。
func (a *Access) CanSeeFile(f *ent.KBFile) bool {
	if !a.Has(f.KBID, rbac.BitView) {
		return false
	}
	return !f.ACLCustom || a.Files[f.ID] || a.Has(f.KBID, rbac.BitManage)
}

// KBIDs 拥有 bit 的知识库 ID。
func (a *Access) KBIDs(bit int) []int {
	out := make([]int, 0, len(a.KB))
	for id, b := range a.KB {
		if b&bit != 0 {
			out = append(out, id)
		}
	}
	return out
}

// AllowedFiles D-27：检索前先算出「本人可见且已就绪」的文件 ID，再在同一条 SQL 里 file_id = ANY(...) 过滤。
// kbIDs 为空表示所有可见知识库。只返回未删除知识库下 status=ready 的文件。
func AllowedFiles(ctx context.Context, db *ent.Client, a *Access, orgID int, kbIDs []int) ([]int, error) {
	view := map[int]bool{}
	for _, id := range a.KBIDs(rbac.BitView) {
		view[id] = true
	}
	var want []int
	if len(kbIDs) == 0 {
		for id := range view {
			want = append(want, id)
		}
	} else {
		for _, id := range kbIDs {
			if view[id] {
				want = append(want, id)
			}
		}
	}
	if len(want) == 0 {
		return nil, nil
	}
	live, err := db.KnowledgeBase.Query().Where(knowledgebase.OrgID(orgID), knowledgebase.IDIn(want...),
		knowledgebase.DeletedAtIsNil()).IDs(ctx)
	if err != nil || len(live) == 0 {
		return nil, err
	}
	var manage, plain []int
	for _, id := range live {
		if a.Has(id, rbac.BitManage) {
			manage = append(manage, id)
		} else {
			plain = append(plain, id)
		}
	}
	custom := make([]int, 0, len(a.Files))
	for id := range a.Files {
		custom = append(custom, id)
	}
	q := db.KBFile.Query().Where(kbfile.OrgID(orgID), kbfile.StatusEQ(kbfile.StatusReady), kbfile.Or(
		kbfile.KBIDIn(manage...),
		kbfile.And(kbfile.KBIDIn(plain...), kbfile.Or(kbfile.ACLCustom(false), kbfile.IDIn(custom...))),
	))
	return q.IDs(ctx)
}
