package audit

import (
	"context"
	"errors"
	"fmt"

	"entgo.io/ent/dialect"

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/auditlog"
)

// 跨进程串行化（E-1 / D-47 第 1 条）：
// PostgreSQL 上每次写入在同一事务内先拿事务级 advisory lock（固定键），再读链尾、算哈希、插入、提交，
// 锁随事务结束自动释放。server、worker 等多个进程因此严格排队，不再出现乐观重试耗尽导致的误拒。
// 哈希格式、prev_hash 唯一索引（兜底防分叉）、Verify 与归档包均不变。
// 拿锁超时或任何写入错误都直接返回错误，由调用方按 fail-closed 拒绝执行。
//
// 调用方已在事务中（EntWriter{C: tx.Client()}）时，锁加在该事务上，持有到外层事务提交/回滚。
// SQLite（仅单测）没有 advisory lock，保持原来的进程内互斥 + 乐观重试行为。

// ChainLockKey 审计链 advisory lock 的固定键（"hdaudit" 的 ASCII）。
// 与迁移锁 72312026 不同，全库共用一条链、只用这一个键。
const ChainLockKey int64 = 0x68646175646974

const (
	chainLockTimeout = "5s"  // 拿锁最多等待时间
	chainStmtTimeout = "15s" // 拿锁后读链尾、插入的语句超时
)

// ErrChainLock 拿审计链锁失败（超时或数据库错误）。
var ErrChainLock = errors.New("audit: chain lock unavailable")

func (w EntWriter) append(ctx context.Context, e Entry, phase auditlog.Phase) error {
	if w.C.SQLDialect() != dialect.Postgres {
		return w.appendOptimistic(ctx, e, phase)
	}
	tx, err := w.C.Tx(ctx)
	switch {
	case errors.Is(err, ent.ErrTxStarted):
		return w.appendInTx(ctx, w.C, e, phase)
	case err != nil:
		return err
	}
	if err := setTimeouts(ctx, tx.Client()); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := lockAndInsert(ctx, tx.Client(), e, phase); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// appendInTx 调用方已开启事务：锁挂在外层事务上；超时设置在本条写完后恢复原值，不影响外层后续语句。
func (w EntWriter) appendInTx(ctx context.Context, c *ent.Client, e Entry, phase auditlog.Phase) error {
	old, err := swapTimeouts(ctx, c, chainLockTimeout, chainStmtTimeout)
	if err != nil {
		return err
	}
	if err := lockAndInsert(ctx, c, e, phase); err != nil {
		return err
	}
	_, err = swapTimeouts(ctx, c, old[0], old[1])
	return err
}

func lockAndInsert(ctx context.Context, c *ent.Client, e Entry, phase auditlog.Phase) error {
	if _, err := c.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, ChainLockKey); err != nil {
		return fmt.Errorf("%w: %w", ErrChainLock, err)
	}
	prev, err := tipOf(ctx, c)
	if err != nil {
		return err
	}
	return insert(ctx, c, prev, e, phase)
}

func setTimeouts(ctx context.Context, c *ent.Client) error {
	_, err := swapTimeouts(ctx, c, chainLockTimeout, chainStmtTimeout)
	return err
}

func swapTimeouts(ctx context.Context, c *ent.Client, lockTO, stmtTO string) ([2]string, error) {
	var old [2]string
	rows, err := c.QueryContext(ctx, `SELECT current_setting('lock_timeout'), current_setting('statement_timeout'),
		set_config('lock_timeout', $1, true), set_config('statement_timeout', $2, true)`, lockTO, stmtTO)
	if err != nil {
		return old, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		err := rows.Err()
		if err == nil {
			err = errors.New("audit: read timeouts")
		}
		return old, err
	}
	var a, b string
	if err := rows.Scan(&old[0], &old[1], &a, &b); err != nil {
		return old, err
	}
	return old, rows.Err()
}

func tipOf(ctx context.Context, c *ent.Client) (string, error) {
	r, err := c.AuditLog.Query().Where(auditlog.HashNEQ("")).Order(ent.Desc(auditlog.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return r.Hash, nil
}

// appendOptimistic SQLite 单测路径：进程内互斥 + 乐观重试（与修复前一致）。
func (w EntWriter) appendOptimistic(ctx context.Context, e Entry, phase auditlog.Phase) error {
	chainMu.Lock()
	defer chainMu.Unlock()
	for i := 0; i < chainRetries; i++ {
		prev, err := tipOf(ctx, w.C)
		if err != nil {
			return err
		}
		err = insert(ctx, w.C, prev, e, phase)
		if err == nil {
			return nil
		}
		if !ent.IsConstraintError(err) {
			return err
		}
	}
	return errors.New("audit: chain contention")
}
