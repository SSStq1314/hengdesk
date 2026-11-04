package api

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/audit"
	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/backup"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/membership"
	"github.com/SSStq1314/hengdesk/server/internal/ent/role"
	"github.com/SSStq1314/hengdesk/server/internal/ent/systemsetting"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/httpserver"
	"github.com/SSStq1314/hengdesk/server/internal/kb"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
	"github.com/SSStq1314/hengdesk/server/internal/tlsinfo"
)

// SystemConfig 管理后台常驻提示所需的配置。
type SystemConfig struct {
	// MemoryBytes 数据库所在机器内存，用于向量索引体积提示（D-42）；0 时读本机 /proc/meminfo（同机部署）。
	MemoryBytes    int64
	BackupDir      string
	BackupKeep     int
	DiskLowPercent int
	// PublicHTTPS 部署声明对外已是 HTTPS（install.sh 导入证书，或前置负载均衡终止 TLS）。
	PublicHTTPS bool
	// TrustedProxies 只有来自这些网段的 X-Forwarded-Proto 才被采信。
	TrustedProxies []*net.IPNet
	// TLSCertFile 部署证书路径，用于自签 / 到期提示（D-34 第 3 点）。
	TLSCertFile string
	// AuditRetentionDays 审计在线保留期（FR-40）。
	AuditRetentionDays int
	// MasterKeyPath 主密钥文件位置（展示给管理员，D-36）；MasterKeyFingerprint 当前主密钥指纹，未配置为空。
	MasterKeyPath        string
	MasterKeyFingerprint string
	// Now 测试用时钟，nil 时取 time.Now。
	Now func() time.Time
}

type systemHandlers struct {
	db  *ent.Client
	cfg SystemConfig
	// kbDB 知识库切片所在的 PostgreSQL；为 nil（内存实现）时不提示索引体积。
	kbDB *sql.DB
}

// SystemStatus GET /admin/system/status 的响应，前端据此显示常驻告警条。
type SystemStatus struct {
	AuditorAssigned  bool     `json:"auditor_assigned"`
	BackupKeep       int      `json:"backup_keep"`
	DiskFreePercent  *float64 `json:"disk_free_percent"`
	DiskLowThreshold int      `json:"disk_low_threshold"`
	DiskLow          bool     `json:"disk_low"`
	HTTPS            bool     `json:"https"`
	// TLS 部署证书信息；读取失败时为 nil（不影响其他提示）。
	TLS *tlsinfo.Info `json:"tls"`
	// MasterKey 主密钥备份提示（D-36）：configured 且未 acknowledged 时前端显示常驻告警条。
	MasterKey MasterKeyStatus `json:"master_key"`
	// KBIndex 向量索引体积（D-42）：over_half 时前端提示索引超过内存一半、检索延迟可能明显上升。读取失败为 nil。
	KBIndex *kb.IndexStatus `json:"kb_index"`
}

// MasterKeyStatus 主密钥状态。不含密钥本身。
type MasterKeyStatus struct {
	Configured     bool       `json:"configured"`
	Path           string     `json:"path"`
	Fingerprint    string     `json:"fingerprint"`
	Acknowledged   bool       `json:"acknowledged"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	AcknowledgedBy string     `json:"acknowledged_by,omitempty"`
	Warning        string     `json:"warning"`
}

const (
	settingMasterKeyAck = "master_key_backup_ack"
	masterKeyWarning    = "丢失后已存的 Key 无法恢复，请与数据库备份分开保管"
)

func (h *systemHandlers) masterKeyStatus(ctx context.Context) MasterKeyStatus {
	st := MasterKeyStatus{Configured: h.cfg.MasterKeyFingerprint != "", Path: h.cfg.MasterKeyPath,
		Fingerprint: h.cfg.MasterKeyFingerprint, Warning: masterKeyWarning}
	if !st.Configured {
		return st
	}
	s, err := h.db.SystemSetting.Query().Where(systemsetting.Key(settingMasterKeyAck)).Only(ctx)
	if err != nil {
		return st
	}
	// 只认当前指纹的确认：轮换后需要重新确认已备份新密钥。
	if fp, _ := s.Value["fingerprint"].(string); fp == st.Fingerprint {
		st.Acknowledged = true
		t := s.UpdatedAt
		st.AcknowledgedAt = &t
		st.AcknowledgedBy, _ = s.Value["by"].(string)
	}
	return st
}

type masterKeyAckInput struct {
	Fingerprint string `json:"fingerprint"`
}

// ackMasterKey POST /admin/system/master-key/ack：管理员确认已把主密钥与数据库备份分开保管。
// 必须回传当前指纹（证明看到的是当前密钥）。设置与审计同事务，审计写失败则不记确认。
func (h *systemHandlers) ackMasterKey(c echo.Context) error {
	var in masterKeyAckInput
	if err := c.Bind(&in); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "请求格式错误")
	}
	if h.cfg.MasterKeyFingerprint == "" {
		return echo.NewHTTPError(http.StatusConflict, "未配置主密钥")
	}
	if in.Fingerprint != h.cfg.MasterKeyFingerprint {
		return echo.NewHTTPError(http.StatusConflict, "主密钥指纹不匹配（可能刚轮换过），请刷新后重新确认")
	}
	ctx := c.Request().Context()
	org, actor, email := auth.Actor(c)
	tx, err := h.db.Tx(ctx)
	if err != nil {
		return err
	}
	err = tx.SystemSetting.Create().SetKey(settingMasterKeyAck).
		SetValue(map[string]any{"fingerprint": in.Fingerprint, "by": email, "user_id": actor}).SetUpdatedBy(actor).
		OnConflictColumns(systemsetting.FieldKey).UpdateNewValues().Exec(ctx)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := (audit.EntWriter{C: tx.Client()}).Write(ctx, audit.Entry{OrgID: org, ActorID: actor, ActorEmail: email,
		Action: "system.master_key.ack", Phase: "result", Outcome: "ok", ResourceType: "master_key", ResourceID: in.Fingerprint,
		IP: c.RealIP(), UserAgent: c.Request().UserAgent(), RequestID: c.Response().Header().Get(echo.HeaderXRequestID),
		Detail: map[string]any{"path": h.cfg.MasterKeyPath}}); err != nil {
		_ = tx.Rollback()
		return audit.ErrRejected
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, h.masterKeyStatus(ctx))
}

func (h *systemHandlers) now() time.Time {
	if h.cfg.Now != nil {
		return h.cfg.Now()
	}
	return time.Now()
}

func (h *systemHandlers) certInfo(c echo.Context) *tlsinfo.Info {
	i, err := tlsinfo.Inspect(h.cfg.TLSCertFile, h.now())
	if err != nil {
		c.Logger().Warnf("tls cert inspect: %v", err)
		return nil
	}
	return &i
}

// tls GET /admin/system/tls：证书主题、SAN、有效期、剩余天数、是否自签。
func (h *systemHandlers) tls(c echo.Context) error {
	i, err := tlsinfo.Inspect(h.cfg.TLSCertFile, h.now())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "证书读取失败")
	}
	return c.JSON(http.StatusOK, i)
}

func (h *systemHandlers) status(c echo.Context) error {
	p := auth.Current(c)
	ctx := c.Request().Context()
	ok, err := h.db.User.Query().Where(user.OrgID(p.OrgID), user.StatusEQ(user.StatusActive),
		user.HasMembershipsWith(membership.HasRoleWith(role.Key(rbac.SecurityAuditor)))).Exist(ctx)
	if err != nil {
		return err
	}
	st := SystemStatus{AuditorAssigned: ok, BackupKeep: h.cfg.BackupKeep, DiskLowThreshold: h.cfg.DiskLowPercent,
		HTTPS: h.cfg.PublicHTTPS || httpserver.IsHTTPS(c.Request(), h.cfg.TrustedProxies), TLS: h.certInfo(c),
		MasterKey: h.masterKeyStatus(ctx)}
	if h.cfg.BackupDir != "" {
		if f, err := backup.FreePercent(h.cfg.BackupDir); err == nil {
			st.DiskFreePercent = &f
			st.DiskLow = h.cfg.DiskLowPercent > 0 && f < float64(h.cfg.DiskLowPercent)
		}
	}
	if h.kbDB != nil {
		if ks, err := kb.CheckIndex(ctx, h.kbDB, h.cfg.MemoryBytes); err == nil {
			st.KBIndex = &ks
		}
	}
	return c.JSON(http.StatusOK, st)
}
