package egress

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 基础设施豁免（D-38 第 6 条）：只有部署配置里声明的数据库、Valkey、对象存储地址不受出站白名单约束。
//
//   - PostgreSQL（DATABASE_URL）与 Valkey（REDIS_ADDR）由各自驱动按配置地址直连，驱动不会跟随重定向去别的地址；
//     TestInfraClientsOnlyFromConfig 保证这些客户端只在启动代码里用配置项构造。
//   - 对象存储走 HTTP，SDK 默认客户端会去任意地址，因此必须使用 InfraHTTP：只能连 S3_ENDPOINT
//     （未配置时为 AWS 官方的 s3.<region>.amazonaws.com 及虚拟主机风格的 <bucket>.s3.<region>.amazonaws.com），
//     其余地址一律拒绝，不跟随重定向，不走环境代理。

// ErrInfraUndeclared 基础设施客户端试图连接部署配置之外的地址。
type ErrInfraUndeclared struct{ Addr string }

func (e *ErrInfraUndeclared) Error() string {
	return fmt.Sprintf("infra egress denied: %s 不是部署配置中声明的基础设施地址", e.Addr)
}

// ObjectStoreAddrs 对象存储在部署配置中声明的地址（host:port，小写）。
func ObjectStoreAddrs(endpoint, region, bucket string) ([]string, error) {
	if strings.TrimSpace(endpoint) == "" {
		if region == "" {
			region = "us-east-1"
		}
		hosts := []string{"s3." + region + ".amazonaws.com:443"}
		if bucket != "" {
			hosts = append(hosts, strings.ToLower(bucket)+".s3."+region+".amazonaws.com:443")
		}
		return hosts, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, fmt.Errorf("S3_ENDPOINT 格式不正确：%q", endpoint)
	}
	return []string{net.JoinHostPort(strings.ToLower(u.Hostname()), fmt.Sprint(portOf(u)))}, nil
}

// InfraHTTP 返回只能连接 addrs（host:port）的 HTTP 客户端，仅供对象存储使用。
func InfraHTTP(addrs []string) *http.Client {
	allowed := map[string]bool{}
	for _, a := range addrs {
		allowed[strings.ToLower(a)] = true
	}
	var d net.Dialer
	d.Timeout = 30 * time.Second
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if !allowed[strings.ToLower(addr)] {
				return nil, &ErrInfraUndeclared{Addr: addr}
			}
			return d.DialContext(ctx, network, addr)
		},
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        32,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
