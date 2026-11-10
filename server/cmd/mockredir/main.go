// mockredir 把白名单内端点 307 到白名单外地址，用于验收脚本第 12 步（AC-09）。
// 思路与真机演示 /workspace/hengdesk-demo/scripts/redirector.py 相同：/meta 指向链路本地元数据地址，其余指向外部域名。
//
//	go run ./cmd/mockredir -addr :18082
package main

import (
	"flag"
	"log"
	"net/http"
	"strings"
)

func main() {
	addr := flag.String("addr", ":18082", "listen address")
	meta := flag.String("meta", "http://169.254.169.254/latest/meta-data/", "Location for /meta*")
	ext := flag.String("external", "https://tickets.partner-example.com/mcp", "Location for other paths")
	flag.Parse()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		loc := *ext
		if strings.HasPrefix(r.URL.Path, "/meta") {
			loc = *meta
		}
		w.Header().Set("Location", loc)
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	log.Printf("mockredir listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
