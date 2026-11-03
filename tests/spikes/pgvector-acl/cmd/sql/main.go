// 极简 SQL 执行器（本机没有 psql）：每个参数一条语句，打印结果行。
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
)

func main() {
	ctx := context.Background()
	c, err := pgx.Connect(ctx, "postgres://postgres@/hengdesk_spike?host=/tmp&port=55432")
	if err != nil {
		panic(err)
	}
	for _, q := range os.Args[1:] {
		rows, err := c.Query(ctx, q)
		if err != nil {
			fmt.Println("ERR", err)
			continue
		}
		for rows.Next() {
			v, _ := rows.Values()
			fmt.Println(v...)
		}
		rows.Close()
	}
}
