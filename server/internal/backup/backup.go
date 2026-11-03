// Package backup 在升级前用 pg_dump 备份数据库；备份失败则中止升级（D-31）。
package backup

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Dumper 可替换，便于测试备份失败场景。
type Dumper interface {
	Dump(ctx context.Context, dsn, file string) error
}

// PGDump 调用 pg_dump（custom 格式），再用 pg_restore --list 校验备份可读。
type PGDump struct{ Bin, RestoreBin string }

func pgEnv(dsn string) (string, []string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", nil, err
	}
	env := os.Environ()
	if pw, ok := u.User.Password(); ok {
		// 密码走环境变量，避免出现在进程参数里。
		env = append(env, "PGPASSWORD="+pw)
		u.User = url.User(u.User.Username())
	}
	return u.String(), env, nil
}

func (p PGDump) Dump(ctx context.Context, dsn, file string) error {
	bin, rbin := p.Bin, p.RestoreBin
	if bin == "" {
		bin = "pg_dump"
	}
	if rbin == "" {
		rbin = "pg_restore"
	}
	clean, env, err := pgEnv(dsn)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, "--format=custom", "--no-owner", "--file", file, "--dbname", clean)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pg_dump: %w: %s", err, out)
	}
	ls := exec.CommandContext(ctx, rbin, "--list", file)
	if out, err := ls.CombinedOutput(); err != nil {
		return fmt.Errorf("备份校验失败: %w: %s", err, out)
	}
	return nil
}

// Run 写入 dir/hengdesk-<时间>-<标签>.dump；先写临时文件，成功后改名。返回备份文件路径。
func Run(ctx context.Context, d Dumper, dsn, dir, label string) (string, error) {
	if dir == "" {
		return "", errors.New("未配置备份目录 HENGDESK_BACKUP_DIR")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建备份目录: %w", err)
	}
	name := fmt.Sprintf("hengdesk-%s-%s.dump", time.Now().UTC().Format("20060102T150405Z"), label)
	final := filepath.Join(dir, name)
	tmp := final + ".partial"
	if err := d.Dump(ctx, dsn, tmp); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	fi, err := os.Stat(tmp)
	if err != nil || fi.Size() == 0 {
		_ = os.Remove(tmp)
		return "", errors.New("备份文件为空")
	}
	if err := os.Rename(tmp, final); err != nil {
		return "", err
	}
	return final, nil
}
