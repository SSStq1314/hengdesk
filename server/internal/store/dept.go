package store

import (
	"context"

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/deptclosure"
)

// CreateDept 创建部门并维护闭包表 dept_closure（自身 depth=0，加上父部门的全部祖先 depth+1）。
// 必须在事务内调用，保证部门与闭包一致。
func CreateDept(ctx context.Context, tx *ent.Tx, orgID int, name string, parentID *int) (*ent.Department, error) {
	d, err := tx.Department.Create().SetOrgID(orgID).SetName(name).SetNillableParentID(parentID).Save(ctx)
	if err != nil {
		return nil, err
	}
	rows := []*ent.DeptClosureCreate{tx.DeptClosure.Create().SetOrgID(orgID).SetAncestorID(d.ID).SetDescendantID(d.ID).SetDepth(0)}
	if parentID != nil {
		anc, err := tx.DeptClosure.Query().Where(deptclosure.DescendantID(*parentID), deptclosure.OrgID(orgID)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, a := range anc {
			rows = append(rows, tx.DeptClosure.Create().SetOrgID(orgID).SetAncestorID(a.AncestorID).SetDescendantID(d.ID).SetDepth(a.Depth+1))
		}
	}
	if err := tx.DeptClosure.CreateBulk(rows...).Exec(ctx); err != nil {
		return nil, err
	}
	return d, nil
}
