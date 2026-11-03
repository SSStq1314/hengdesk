// Package tlsinfo 读取部署证书的到期与自签信息，供管理后台提示（D-34 第 3 点）。
package tlsinfo

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"time"
)

// WarnDays 到期前多少天开始提醒。
const WarnDays = 30

// Info GET /admin/system/tls 的响应。
type Info struct {
	// Present 是否部署了证书（nginx 终止 TLS）。为 false 时其余字段无意义。
	Present    bool      `json:"present"`
	SelfSigned bool      `json:"self_signed"`
	Subject    string    `json:"subject,omitempty"`
	Issuer     string    `json:"issuer,omitempty"`
	SANs       []string  `json:"sans,omitempty"`
	NotBefore  time.Time `json:"not_before,omitzero"`
	NotAfter   time.Time `json:"not_after,omitzero"`
	DaysLeft   int       `json:"days_left"`
	// Expiring 剩余不足 WarnDays 天（含已过期）。
	Expiring bool `json:"expiring"`
	Expired  bool `json:"expired"`
	WarnDays int  `json:"warn_days"`
}

// Inspect 读取 PEM 证书链的第一张证书。文件不存在返回 Present=false 且无错误。
func Inspect(path string, now time.Time) (Info, error) {
	info := Info{WarnDays: WarnDays}
	if path == "" {
		return info, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return info, nil
	}
	if err != nil {
		return info, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return info, nil
	}
	return Parse(raw, now)
}

// Parse 解析 PEM 证书。
func Parse(raw []byte, now time.Time) (Info, error) {
	info := Info{WarnDays: WarnDays}
	var b *pem.Block
	for {
		b, raw = pem.Decode(raw)
		if b == nil || b.Type == "CERTIFICATE" {
			break
		}
	}
	if b == nil {
		return info, fmt.Errorf("tlsinfo: no certificate in PEM")
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return info, fmt.Errorf("tlsinfo: %w", err)
	}
	info.Present = true
	info.Subject, info.Issuer = c.Subject.String(), c.Issuer.String()
	info.SelfSigned = bytes.Equal(c.RawIssuer, c.RawSubject) && c.CheckSignature(c.SignatureAlgorithm, c.RawTBSCertificate, c.Signature) == nil
	info.SANs = append(info.SANs, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		info.SANs = append(info.SANs, ip.String())
	}
	info.NotBefore, info.NotAfter = c.NotBefore.UTC(), c.NotAfter.UTC()
	left := c.NotAfter.Sub(now)
	info.DaysLeft = int(math.Floor(left.Hours() / 24))
	info.Expired = left <= 0
	info.Expiring = left < WarnDays*24*time.Hour
	return info, nil
}
