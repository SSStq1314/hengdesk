package cli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auditarchive"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/config"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/objstore"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
	"github.com/SSStq1314/hengdesk/server/internal/store"
)

// auditCmd 审计保留期与归档（FR-40）。
func auditCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "audit", Short: "审计日志保留期、归档与离线校验"}
	cmd.AddCommand(auditStatusCmd(), auditArchiveCmd(), auditVerifyArchiveCmd(), auditFetchArchiveCmd(), auditKeygenCmd())
	return cmd
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func auditStatusCmd() *cobra.Command {
	var remind bool
	cmd := &cobra.Command{Use: "status", Short: "显示保留期状态；--remind 时在 30 天内到期则给审计员发站内提醒",
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
			ctx := context.Background()
			if remind {
				sent, st, err := auditarchive.Remind(ctx, client, audit.EntWriter{C: client}, cfg.AuditRetentionDays, time.Now())
				if err != nil {
					return err
				}
				return printJSON(map[string]any{"status": st, "reminded": sent})
			}
			st, err := auditarchive.Check(ctx, client, cfg.AuditRetentionDays, time.Now())
			if err != nil {
				return err
			}
			return printJSON(st)
		}}
	cmd.Flags().BoolVar(&remind, "remind", false, "到期前 30 天内时写一条审计员站内提醒（同一批记录 7 天内只提醒一次）")
	return cmd
}

func loadSignKey(path string) (ed25519.PrivateKey, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取签名私钥: %w", err)
	}
	seed, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("签名私钥格式错误：应为 hex 编码的 32 字节种子（用 hengdesk audit keygen 生成）")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func auditArchiveCmd() *cobra.Command {
	var auditor string
	var yes bool
	cmd := &cobra.Command{Use: "archive", Short: "把超过保留期的审计记录归档到对象存储，写归档断点后再从在线表移除（需审计员执行）",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if auditor == "" {
				return errors.New("必须用 --auditor 指定执行归档的安全审计员邮箱")
			}
			key, err := loadSignKey(cfg.AuditArchiveSignKeyFile)
			if err != nil {
				return err
			}
			client, db, err := store.Open(cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
			defer cancel()
			u, err := client.User.Query().Where(user.Email(strings.ToLower(auditor)), user.StatusEQ(user.StatusActive)).Only(ctx)
			if err != nil {
				return fmt.Errorf("找不到启用状态的用户 %s: %w", auditor, err)
			}
			p, err := auth.LoadPrincipal(ctx, client, u.ID)
			if err != nil {
				return err
			}
			if !p.Perms.Has(rbac.AuditExport) {
				return fmt.Errorf("%s 不是安全审计员（缺少 %s 权限），拒绝归档", auditor, rbac.AuditExport)
			}
			st, err := auditarchive.Check(ctx, client, cfg.AuditRetentionDays, time.Now())
			if err != nil {
				return err
			}
			if !st.Overdue {
				return printJSON(map[string]any{"archived": false, "reason": "没有超过保留期的记录", "status": st})
			}
			if !yes {
				return printJSON(map[string]any{"archived": false, "reason": "预览：加 --yes 执行归档", "status": st})
			}
			s3, err := objstore.New(objstore.Config{Endpoint: cfg.S3Endpoint, Region: cfg.S3Region, AccessKey: cfg.S3AccessKey,
				SecretKey: cfg.S3SecretKey, Bucket: cfg.S3Bucket, PathStyle: cfg.S3PathStyle})
			if err != nil {
				return err
			}
			a := &auditarchive.Archiver{C: client, DB: db, W: audit.EntWriter{C: client}, S: s3,
				RetentionDays: cfg.AuditRetentionDays, SignKey: key}
			res, err := a.Run(ctx, audit.Entry{OrgID: p.OrgID, ActorID: p.ID, ActorEmail: p.Email, UserAgent: "hengdesk-cli"})
			if err != nil {
				return err
			}
			return printJSON(res)
		}}
	cmd.Flags().StringVar(&auditor, "auditor", "", "执行归档的安全审计员邮箱（记入归档断点）")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认执行（不加只预览）")
	return cmd
}

func auditVerifyArchiveCmd() *cobra.Command {
	var pub string
	cmd := &cobra.Command{Use: "verify-archive <归档.tar.gz>", Short: "离线校验归档包（不连接数据库与网络）", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			var pk ed25519.PublicKey
			if pub != "" {
				b, err := hex.DecodeString(strings.TrimSpace(pub))
				if err != nil || len(b) != ed25519.PublicKeySize {
					return errors.New("--pubkey 应为 hex 编码的 32 字节 Ed25519 公钥")
				}
				pk = b
			}
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			rep, err := audit.VerifyPackage(f, pk)
			if err != nil {
				return fmt.Errorf("校验失败: %w", err)
			}
			fmt.Printf("OK 归档 %s：%d 条（ID %d–%d），哈希链 %s … %s，签名 %s\n", rep.Manifest.ArchiveID, rep.Manifest.Count,
				rep.Manifest.FirstID, rep.Manifest.LastID, short(rep.Manifest.FirstHash), short(rep.Manifest.LastHash), rep.Signature)
			return nil
		}}
	cmd.Flags().StringVar(&pub, "pubkey", "", "Ed25519 公钥（hex），校验 manifest.sig")
	return cmd
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func auditKeygenCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{Use: "keygen", Short: "生成归档包签名密钥（私钥写入文件，公钥打印到标准输出）",
		RunE: func(_ *cobra.Command, _ []string) error {
			if out == "" {
				return errors.New("必须用 --out 指定私钥文件")
			}
			pubk, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				return err
			}
			f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintln(f, hex.EncodeToString(priv.Seed())); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			fmt.Println(hex.EncodeToString(pubk))
			return nil
		}}
	cmd.Flags().StringVar(&out, "out", "", "私钥文件路径（不存在才会创建，权限 600）")
	return cmd
}

func auditFetchArchiveCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{Use: "fetch-archive <对象键>", Short: "从对象存储下载归档包到本地文件（对象键见 archive 输出的 object_key）", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if out == "" {
				return errors.New("必须用 --out 指定本地文件")
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			s3, err := objstore.New(objstore.Config{Endpoint: cfg.S3Endpoint, Region: cfg.S3Region, AccessKey: cfg.S3AccessKey,
				SecretKey: cfg.S3SecretKey, Bucket: cfg.S3Bucket, PathStyle: cfg.S3PathStyle})
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			rc, err := s3.Get(ctx, args[0])
			if err != nil {
				return err
			}
			defer func() { _ = rc.Close() }()
			f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, rc); err != nil {
				_ = f.Close()
				return err
			}
			return f.Close()
		}}
	cmd.Flags().StringVar(&out, "out", "", "本地文件路径（不存在才会创建）")
	return cmd
}
