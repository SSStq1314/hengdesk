package api

import (
	"net"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/SSStq1314/hengdesk/server/internal/auth"
	"github.com/SSStq1314/hengdesk/server/internal/backup"
	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/membership"
	"github.com/SSStq1314/hengdesk/server/internal/ent/role"
	"github.com/SSStq1314/hengdesk/server/internal/ent/user"
	"github.com/SSStq1314/hengdesk/server/internal/httpserver"
	"github.com/SSStq1314/hengdesk/server/internal/rbac"
	"github.com/SSStq1314/hengdesk/server/internal/tlsinfo"
)

// SystemConfig 管理后台常驻提示所需的配置。
type SystemConfig struct {
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
	// Now 测试用时钟，nil 时取 time.Now。
	Now func() time.Time
}

type systemHandlers struct {
	db  *ent.Client
	cfg SystemConfig
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
		HTTPS: h.cfg.PublicHTTPS || httpserver.IsHTTPS(c.Request(), h.cfg.TrustedProxies), TLS: h.certInfo(c)}
	if h.cfg.BackupDir != "" {
		if f, err := backup.FreePercent(h.cfg.BackupDir); err == nil {
			st.DiskFreePercent = &f
			st.DiskLow = h.cfg.DiskLowPercent > 0 && f < float64(h.cfg.DiskLowPercent)
		}
	}
	return c.JSON(http.StatusOK, st)
}
