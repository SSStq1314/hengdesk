package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/config"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/store"
)

// keysCmd 主密钥运维（D-36）。
func keysCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "keys", Short: "模型网关主密钥：自检与轮换"}
	cmd.AddCommand(keysCheckCmd(), keysRotateCmd())
	return cmd
}

func keysCheckCmd() *cobra.Command {
	return &cobra.Command{Use: "check", Short: "检查主密钥能否解密库中全部上游 API Key",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			client, _, err := store.Open(cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			k, err := gateway.LoadKeyring(cfg.MasterKeyFile, cfg.MasterKey)
			if err != nil {
				return masterKeyLoadError(cfg.MasterKeyFile, err)
			}
			if err := gateway.SelfCheck(context.Background(), client, k, cfg.Env == "prod"); err != nil {
				return err
			}
			fmt.Printf("主密钥正常：文件 %s，指纹 %s\n", cfg.MasterKeyFile, k.Fingerprint())
			return nil
		}}
}

func masterKeyLoadError(file string, err error) error {
	return fmt.Errorf("主密钥无法加载（%s）: %v。\n处理：确认文件存在、属主为运行用户（容器内 uid 10001）、权限 600，内容为 64 位 hex；"+
		"安装时生成于部署目录 secrets/master.key（见 docs/ops/model-gateway.md）", file, err)
}

func keysRotateCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{Use: "rotate-master", Short: "生成新主密钥并在一个事务内重新加密全部上游 API Key（含审计）",
		Long: `轮换步骤：
  1. 用当前主密钥解密全部上游 API Key（任何一条失败则不做任何修改）；
  2. 旧密钥备份为 <主密钥文件>.bak.<时间戳>，新密钥先写到 <主密钥文件>.new；
  3. 在一个数据库事务里用新密钥重写全部密文并写审计（model.master_key.rotate），任何一步失败整体回滚；
  4. 提交后把 .new 换成正式文件。失败时删除 .new，旧密钥保持有效。
轮换前请停止 server（docker compose stop server），完成后再启动，并重新备份新的主密钥。`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				return errors.New("轮换会替换主密钥文件，请先停止 server 并确认已备份当前密钥，然后加 --yes 执行")
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.MasterKeyFile == "" {
				return errors.New("轮换要求主密钥以文件方式配置（HENGDESK_MASTER_KEY_FILE）；用环境变量传入的密钥请先迁移到文件（见 docs/ops/model-gateway.md）")
			}
			oldK, err := gateway.LoadKeyring(cfg.MasterKeyFile, "")
			if err != nil {
				return masterKeyLoadError(cfg.MasterKeyFile, err)
			}
			client, _, err := store.Open(cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			out := cmd.OutOrStdout()
			res, err := rotateMaster(context.Background(), client, cfg.MasterKeyFile, oldK, time.Now())
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(out, "主密钥已轮换：重新加密 %d 个供应商的 API Key\n  旧指纹 %s → 新指纹 %s\n  旧密钥备份：%s\n",
				len(res.Providers), oldK.Fingerprint(), res.NewFingerprint, res.Backup)
			_, _ = fmt.Fprintln(out, "下一步：启动 server，并把新的主密钥与数据库备份分开保管；确认新密钥可用后再妥善销毁旧备份。管理后台会再次要求确认已备份。")
			return nil
		}}
	cmd.Flags().BoolVar(&yes, "yes", false, "确认执行")
	return cmd
}

type rotateResult struct {
	Providers      []string
	NewFingerprint string
	Backup         string
}

// rotateMaster 轮换主密钥文件 path（见 keysRotateCmd 的步骤说明）。
func rotateMaster(ctx context.Context, client *ent.Client, path string, oldK *gateway.Keyring, now time.Time) (*rotateResult, error) {
	oldRaw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	origSt, _ := os.Stat(path) // keytool 容器以 root 运行：新文件与备份要还给原属主，宿主机部署用户才读得到
	hexKey, err := gateway.GenerateMasterKey()
	if err != nil {
		return nil, err
	}
	raw, _ := gateway.ParseMasterKey(hexKey)
	newK, err := gateway.NewKeyring(raw)
	if err != nil {
		return nil, err
	}
	bak := fmt.Sprintf("%s.bak.%s", path, now.UTC().Format("20060102T150405Z"))
	if err := writeFileExcl(bak, oldRaw); err != nil {
		return nil, fmt.Errorf("写旧密钥备份失败（%s）: %w", bak, err)
	}
	tmp := path + ".new"
	_ = os.Remove(tmp)
	if err := writeFileExcl(tmp, []byte(hexKey+"\n")); err != nil {
		return nil, fmt.Errorf("写新密钥失败（%s）: %w", tmp, err)
	}
	var orgID int
	if o, err := client.Organization.Query().First(ctx); err == nil {
		orgID = o.ID
	}
	names, err := gateway.Rotate(ctx, client, oldK, newK, func(ctx context.Context, tx *ent.Tx, ps []string) error {
		return audit.EntWriter{C: tx.Client()}.Write(ctx, audit.Entry{OrgID: orgID, ActorEmail: "system:cli",
			Action: "model.master_key.rotate", Phase: "result", Outcome: "ok", ResourceType: "master_key",
			ResourceID: filepath.Base(path), Detail: map[string]any{"providers": ps, "count": len(ps),
				"old_fingerprint": oldK.Fingerprint(), "new_fingerprint": newK.Fingerprint(), "backup": filepath.Base(bak)}})
	})
	if err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("%w\n旧主密钥仍然有效，未做任何修改", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, fmt.Errorf("数据库已用新密钥重新加密，但替换密钥文件失败: %v。\n"+
			"处理：手工执行 mv %s %s（新密钥在 .new 文件里），否则 server 启动自检会失败", err, tmp, path)
	}
	if origSt != nil {
		chownLike(origSt, path, bak)
	}
	return &rotateResult{Providers: names, NewFingerprint: newK.Fingerprint(), Backup: bak}, nil
}

func writeFileExcl(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
