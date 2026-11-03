// Package queue 基于 asynq + Valkey 的异步任务。
package queue

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/hibiken/asynq"
)

// TypePing 是 M0 内置的探活任务。
const TypePing = "system:ping"

// RedisOpt 由地址构造连接参数（Valkey 兼容 Redis 协议）。
func RedisOpt(addr string) asynq.RedisClientOpt { return asynq.RedisClientOpt{Addr: addr} }

// NewPing 构造探活任务。
func NewPing(msg string) (*asynq.Task, error) {
	b, err := json.Marshal(map[string]string{"msg": msg})
	if err != nil {
		return nil, err
	}
	return asynq.NewTask(TypePing, b), nil
}

// Mux 注册内置处理器。
func Mux(lg *slog.Logger) *asynq.ServeMux {
	m := asynq.NewServeMux()
	m.HandleFunc(TypePing, func(_ context.Context, t *asynq.Task) error {
		lg.Info("ping task", "payload", string(t.Payload()))
		return nil
	})
	return m
}
