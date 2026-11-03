// mockupstream 在本地端口运行复现 DeepSeek 行为的模拟上游，用于演示与手工联调。
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/SSStq1314/hengdesk/server/internal/gateway/mockupstream"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18080", "监听地址")
	key := flag.String("key", "sk-demo", "期望的 API Key")
	flag.Parse()
	_, h := mockupstream.NewHandler(*key)
	log.Printf("mock upstream listening on http://%s", *addr)
	srv := &http.Server{Addr: *addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
