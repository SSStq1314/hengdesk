// 探测 pdfcpu 是否提供可用文本抽取：它只能导出原始内容流（含 Tj/TJ 操作符与 CID 编码字节）。
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

func main() {
	out, _ := os.MkdirTemp("", "pdfcpu")
	if err := api.ExtractContentFile(context.Background(), os.Args[1], out, nil, nil); err != nil {
		fmt.Println("err:", err)
		return
	}
	es, _ := os.ReadDir(out)
	for _, e := range es {
		b, _ := os.ReadFile(out + "/" + e.Name())
		if len(b) > 300 {
			b = b[:300]
		}
		fmt.Printf("%s:\n%q\n", e.Name(), b)
	}
}
