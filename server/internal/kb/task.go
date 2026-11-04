package kb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hibiken/asynq"

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/kbfile"
)

const (
	// TaskIndex 解析并索引一个知识库文件，payload {"file_id": N}。
	TaskIndex = "kb:index"
	// TaskSweep 周期补偿：重新投递入队失败的 pending 文件，回收卡在 parsing/indexing 的文件（worker 崩溃）。
	TaskSweep = "kb:sweep"
)

// Enqueuer 投递索引任务。
type Enqueuer interface {
	Enqueue(ctx context.Context, fileID int) error
}

// AsynqEnqueuer 生产实现。
type AsynqEnqueuer struct{ C *asynq.Client }

func (q AsynqEnqueuer) Enqueue(ctx context.Context, fileID int) error {
	b, _ := json.Marshal(map[string]int{"file_id": fileID})
	_, err := q.C.EnqueueContext(ctx, asynq.NewTask(TaskIndex, b), asynq.MaxRetry(3), asynq.Timeout(30*time.Minute))
	return err
}

// InlineEnqueuer 同步执行（测试与未配置 Valkey 的开发环境）。
type InlineEnqueuer struct{ X *Indexer }

func (q InlineEnqueuer) Enqueue(ctx context.Context, fileID int) error {
	return q.X.Index(context.WithoutCancel(ctx), fileID)
}

// HandleIndex asynq 处理函数。
func (x *Indexer) HandleIndex(ctx context.Context, t *asynq.Task) error {
	var p struct {
		FileID int `json:"file_id"`
	}
	if err := json.Unmarshal(t.Payload(), &p); err != nil || p.FileID <= 0 {
		return fmt.Errorf("bad payload: %w", asynq.SkipRetry)
	}
	return x.Index(ctx, p.FileID)
}

// Sweep pending 超过 2 分钟的重新投递；parsing/indexing 超过 40 分钟（超过任务超时）的退回 pending 再投递。
func Sweep(ctx context.Context, db *ent.Client, q Enqueuer, now time.Time) (int, error) {
	if _, err := db.KBFile.Update().Where(kbfile.StatusIn(kbfile.StatusParsing, kbfile.StatusIndexing),
		kbfile.UpdatedAtLT(now.Add(-40*time.Minute))).SetStatus(kbfile.StatusPending).Save(ctx); err != nil {
		return 0, err
	}
	ids, err := db.KBFile.Query().Where(kbfile.StatusEQ(kbfile.StatusPending), kbfile.UpdatedAtLT(now.Add(-2*time.Minute))).
		Limit(500).IDs(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		if err := q.Enqueue(ctx, id); err == nil {
			n++
		}
	}
	return n, nil
}
