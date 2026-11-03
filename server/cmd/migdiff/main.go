// Command migdiff 根据 ent schema 生成 Atlas 版本化迁移文件（开发与 CI 使用，不随产品发布）。
//
//	go run ./cmd/migdiff -dev postgres://... -name add_xxx     # 生成新迁移
//	go run ./cmd/migdiff -dev postgres://... -check            # CI：schema 与迁移目录不一致则失败
//
// -dev 指向一个可创建临时库的 PostgreSQL（需 pgvector 扩展）；工具会新建并删除临时数据库。
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"time"

	atlasmigrate "ariga.io/atlas/sql/migrate"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/schema"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/SSStq1314/hengdesk/server/internal/ent/migrate"
)

func main() {
	dev := flag.String("dev", os.Getenv("HENGDESK_DEV_DATABASE_URL"), "开发库连接串")
	name := flag.String("name", "", "迁移名称（snake_case）")
	check := flag.Bool("check", false, "只检查，不一致则退出码 1")
	dirPath := flag.String("dir", "internal/store/migrations", "迁移目录")
	hashOnly := flag.Bool("hash", false, "只重算 atlas.sum（手写迁移后使用，无需数据库）")
	flag.Parse()
	if *hashOnly {
		if err := rehash(*dirPath); err != nil {
			fmt.Fprintln(os.Stderr, "migdiff:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*dev, *name, *dirPath, *check); err != nil {
		fmt.Fprintln(os.Stderr, "migdiff:", err)
		os.Exit(1)
	}
}

func run(dev, name, dirPath string, check bool) error {
	if dev == "" {
		return errors.New("需要 -dev 或 HENGDESK_DEV_DATABASE_URL")
	}
	if !check && name == "" {
		return errors.New("需要 -name")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, err := sql.Open("pgx", dev)
	if err != nil {
		return err
	}
	defer func() { _ = admin.Close() }()
	tmp := fmt.Sprintf("hd_migdiff_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+tmp); err != nil {
		return fmt.Errorf("创建临时库: %w", err)
	}
	defer func() { _, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+tmp+" WITH (FORCE)") }()
	u, err := url.Parse(dev)
	if err != nil {
		return err
	}
	u.Path = "/" + tmp
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	var dir atlasmigrate.Dir
	local, err := atlasmigrate.NewLocalDir(dirPath)
	if err != nil {
		return err
	}
	dir = local
	if !check {
		// 等价于 atlas migrate hash：允许手写迁移（如触发器），生成前重算 atlas.sum。
		if err := rehash(dirPath); err != nil {
			return err
		}
	}
	if check {
		// 在内存副本上 diff，避免改动工作区。
		mem := &atlasmigrate.MemDir{}
		files, err := local.Files()
		if err != nil {
			return err
		}
		for _, f := range files {
			if err := mem.WriteFile(f.Name(), f.Bytes()); err != nil {
				return err
			}
		}
		sum, err := local.Checksum()
		if err != nil {
			return err
		}
		if err := atlasmigrate.WriteSumFile(mem, sum); err != nil {
			return err
		}
		if err := atlasmigrate.Validate(local); err != nil {
			return fmt.Errorf("atlas.sum 校验失败（迁移文件被手改？）: %w", err)
		}
		dir = mem
		name = "check"
	}
	m, err := schema.NewMigrate(entsql.OpenDB(dialect.Postgres, db),
		schema.WithDir(dir),
		schema.WithMigrationMode(schema.ModeReplay),
		schema.WithDialect(dialect.Postgres),
		schema.WithFormatter(atlasmigrate.DefaultFormatter),
		schema.WithErrNoPlan(true),
		schema.WithDropColumn(true),
		schema.WithDropIndex(true),
	)
	if err != nil {
		return err
	}
	err = m.NamedDiff(ctx, name, migrate.Tables...)
	switch {
	case errors.Is(err, atlasmigrate.ErrNoPlan):
		fmt.Println("migdiff: schema 与迁移目录一致")
		return nil
	case err != nil:
		return err
	case check:
		return errors.New("ent schema 有未生成的迁移，请运行 go run ./cmd/migdiff -name <名称> 并提交")
	default:
		fmt.Println("migdiff: 已生成新迁移，请检查后提交")
		return nil
	}
}

func rehash(dirPath string) error {
	local, err := atlasmigrate.NewLocalDir(dirPath)
	if err != nil {
		return err
	}
	sum, err := local.Checksum()
	if err != nil {
		return err
	}
	return atlasmigrate.WriteSumFile(local, sum)
}
