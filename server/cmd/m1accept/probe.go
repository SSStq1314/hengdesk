package main

import (
	"context"
	"fmt"
	"net"
	"time"
)

// probeNoPublicNet 断言本进程（验收容器内）到外网解析器与公网地址不可达。
// 只在 CI/验收机装了 iptables 断网模拟层后启用（M1ACCEPT_REQUIRE_NO_PUBLIC_NET），
// 不改产品出站控制（D-47 DNS 默认 local_verify）。
func probeNoPublicNet() error {
	deadline := 2 * time.Second
	// 直接打外网解析器与常用公网口，必须失败。
	for _, addr := range []string{"8.8.8.8:53", "1.1.1.1:53", "8.8.8.8:443", "1.1.1.1:443"} {
		c, err := net.DialTimeout("tcp", addr, deadline)
		if err == nil {
			_ = c.Close()
			return fmt.Errorf("断网验收：TCP %s 不应连通（对外网出站应为 0）", addr)
		}
	}
	for _, addr := range []string{"8.8.8.8:53", "1.1.1.1:53"} {
		if err := probeUDPQuery(addr, deadline); err != nil {
			return err
		}
	}
	// 经系统解析器查外网域名：若 Docker 内嵌 DNS（127.0.0.11）经宿主机上游答了 A 记录，
	// 那是 dockerd 在宿主机上的解析，不是容器 DOCKER-USER 出站；但连这些地址必须失败。
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, "example.com")
	if err != nil {
		fmt.Println("外网域名 example.com 解析失败（预期）")
		return nil
	}
	if len(ips) == 0 {
		fmt.Println("外网域名 example.com 无记录（预期）")
		return nil
	}
	fmt.Printf("注意：example.com 经本机解析器返回 %d 个地址（可能经 Docker 内嵌 DNS 的宿主机上游），将断言连接失败\n", len(ips))
	for _, ip := range ips {
		addr := net.JoinHostPort(ip.IP.String(), "443")
		c, err := net.DialTimeout("tcp", addr, deadline)
		if err == nil {
			_ = c.Close()
			return fmt.Errorf("断网验收：连外网地址 %s 不应成功", addr)
		}
	}
	return nil
}

func probeUDPQuery(addr string, d time.Duration) error {
	c, err := net.DialTimeout("udp", addr, d)
	if err != nil {
		return nil
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(d))
	// 最小 DNS 查询：example.com IN A（不含递归依赖）。
	q := []byte{
		0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0,
		0x00, 0x01, 0x00, 0x01,
	}
	if _, err := c.Write(q); err != nil {
		return nil
	}
	buf := make([]byte, 512)
	n, err := c.Read(buf)
	if err == nil && n > 0 {
		return fmt.Errorf("断网验收：UDP %s 不应收到外网 DNS 应答", addr)
	}
	return nil
}
