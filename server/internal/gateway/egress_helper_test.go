package gateway_test

import (
	"github.com/SSStq1314/hengdesk/server/internal/egress"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
)

// testClient 测试用网关客户端：同样经过出站控制，只放行本机 mock 上游。
func testClient() *gateway.Client {
	return &gateway.Client{HTTP: (&egress.Client{Policy: egress.MustStatic("cidr 127.0.0.0/8")}).HTTP()}
}
