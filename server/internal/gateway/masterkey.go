package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/modelprovider"
)

// 主密钥运维（D-36）：启动自检与轮换。

// GenerateMasterKey 生成新的 32 字节主密钥，返回 hex 编码（与 install.sh 生成的格式一致）。
func GenerateMasterKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// SelfCheck 启动自检：库里已有上游 Key 密文时，主密钥必须存在且能解开每一条。
// requireKey=true（生产环境）时即使还没有密文也要求配置主密钥。
// 返回的错误带中文处理指引，直接打印给运维。
func SelfCheck(ctx context.Context, db *ent.Client, k *Keyring, requireKey bool) error {
	ps, err := db.ModelProvider.Query().Where(modelprovider.APIKeyEncNotNil()).All(ctx)
	if err != nil {
		return fmt.Errorf("主密钥自检：读取供应商失败: %w", err)
	}
	var enc []*ent.ModelProvider
	for _, p := range ps {
		if len(p.APIKeyEnc) > 0 {
			enc = append(enc, p)
		}
	}
	if k == nil {
		if len(enc) > 0 {
			return fmt.Errorf("主密钥自检失败：数据库中有 %d 个供应商保存了加密的 API Key，但没有配置主密钥。\n"+
				"处理：把安装时生成的 secrets/master.key 放回原位（容器内路径由 HENGDESK_MASTER_KEY_FILE 指定）后重启；"+
				"如果主密钥已经丢失，已存的 Key 无法恢复，只能在管理后台为每个供应商重新填写 API Key（见 docs/ops/model-gateway.md）", len(enc))
		}
		if requireKey {
			return errors.New("主密钥自检失败：生产环境必须配置主密钥（HENGDESK_MASTER_KEY_FILE）。\n" +
				"处理：重新运行 install.sh 生成 secrets/master.key，或手工生成 32 字节 hex 密钥写入该文件（权限 600）")
		}
		return nil
	}
	var bad []string
	for _, p := range enc {
		if _, err := k.Open(p.Name, p.APIKeyEnc); err != nil {
			bad = append(bad, p.Name)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("主密钥自检失败：当前主密钥（指纹 %s）无法解密 %d 个供应商的 API Key：%s。\n"+
			"处理：确认挂载的是原来的 secrets/master.key（轮换后请使用新文件，旧文件在 master.key.bak.*）；"+
			"如果确实丢失，只能在管理后台为这些供应商重新填写 API Key（见 docs/ops/model-gateway.md）",
			k.Fingerprint(), len(bad), strings.Join(bad, "、"))
	}
	return nil
}

// RotateAuditFunc 在轮换事务内写审计；返回错误则整个轮换回滚（审计写失败就拒绝执行）。
type RotateAuditFunc func(ctx context.Context, tx *ent.Tx, providers []string) error

// Rotate 用新主密钥重新加密全部上游 Key。先用旧密钥解开全部密文（任何一条失败则不做任何修改），
// 然后在一个事务里逐条重写并写审计，任何一步失败整体回滚。返回重新加密的供应商名。
func Rotate(ctx context.Context, db *ent.Client, oldK, newK *Keyring, auditFn RotateAuditFunc) ([]string, error) {
	if oldK == nil || newK == nil {
		return nil, ErrNoMasterKey
	}
	if oldK.Fingerprint() == newK.Fingerprint() {
		return nil, errors.New("新旧主密钥相同")
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	rollback := func(err error) ([]string, error) {
		_ = tx.Rollback()
		return nil, err
	}
	ps, err := tx.ModelProvider.Query().Where(modelprovider.APIKeyEncNotNil()).All(ctx)
	if err != nil {
		return rollback(err)
	}
	var names []string
	for _, p := range ps {
		if len(p.APIKeyEnc) == 0 {
			continue
		}
		pt, err := oldK.Open(p.Name, p.APIKeyEnc)
		if err != nil {
			return rollback(fmt.Errorf("旧主密钥无法解密供应商 %q 的 API Key，轮换已取消（未做任何修改）", p.Name))
		}
		enc, err := newK.Seal(p.Name, pt)
		if err != nil {
			return rollback(err)
		}
		// 写入前自校验：新密文能被新密钥解开。
		if back, err := newK.Open(p.Name, enc); err != nil || back != pt {
			return rollback(errors.New("新密文自校验失败，轮换已取消"))
		}
		if err := tx.ModelProvider.UpdateOneID(p.ID).SetAPIKeyEnc(enc).Exec(ctx); err != nil {
			return rollback(err)
		}
		names = append(names, p.Name)
	}
	if auditFn != nil {
		if err := auditFn(ctx, tx, names); err != nil {
			return rollback(fmt.Errorf("审计写入失败，轮换已回滚: %w", err))
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("提交失败，轮换已回滚: %w", err)
	}
	return names, nil
}
