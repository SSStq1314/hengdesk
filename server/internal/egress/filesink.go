package egress

import (
	"context"
	"encoding/json"
	"io"
	"sync"
)

// JSONLSink 把出站日志按行写成 JSON（D-39 第 3 条：没有数据库的命令行工具，如 cmd/cassette-record，
// 也必须经统一出站客户端并留出站日志）。写失败时 Start 返回错误，未声明审计 fail-closed 的调用会被拒绝。
type JSONLSink struct {
	W  io.Writer
	mu sync.Mutex
	n  int
}

type jsonlRecord struct {
	Phase string `json:"phase"` // start / result
	Record
}

func (s *JSONLSink) write(phase string, r Record) error {
	b, err := json.Marshal(jsonlRecord{Phase: phase, Record: r})
	if err != nil {
		return err
	}
	_, err = s.W.Write(append(b, '\n'))
	return err
}

// Start 记录即将发出的请求。
func (s *JSONLSink) Start(_ context.Context, r Record) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	r.ID = s.n
	return s.n, s.write("start", r)
}

// Write 记录请求结果或拒绝。
func (s *JSONLSink) Write(_ context.Context, r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.ID == 0 {
		s.n++
		r.ID = s.n
	}
	return s.write("result", r)
}
