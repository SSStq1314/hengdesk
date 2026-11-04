package gateway_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/gateway"
	"github.com/SSStq1314/hengdesk/server/internal/store/storetest"
)

func kr(t *testing.T, hexKey string) *gateway.Keyring {
	t.Helper()
	k, err := gateway.LoadKeyring("", hexKey)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSelfCheck(t *testing.T) {
	c := storetest.NewTestClient(t)
	ctx := context.Background()
	k1, k2 := kr(t, strings.Repeat("11", 32)), kr(t, strings.Repeat("22", 32))
	if k1.Fingerprint() == k2.Fingerprint() || len(k1.Fingerprint()) != 16 {
		t.Fatal("fingerprint")
	}
	// 没有密文：dev 不要求密钥，prod 要求
	if err := gateway.SelfCheck(ctx, c, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := gateway.SelfCheck(ctx, c, nil, true); err == nil || !strings.Contains(err.Error(), "生产环境必须配置主密钥") {
		t.Fatalf("prod: %v", err)
	}
	enc, _ := k1.Seal("ds", "sk-secret")
	c.ModelProvider.Create().SetName("ds").SetBaseURL("http://x").SetAPIKeyEnc(enc).ExecX(ctx)
	c.ModelProvider.Create().SetName("nokey").SetBaseURL("http://x").ExecX(ctx)
	if err := gateway.SelfCheck(ctx, c, nil, false); err == nil || !strings.Contains(err.Error(), "没有配置主密钥") {
		t.Fatalf("missing: %v", err)
	}
	if err := gateway.SelfCheck(ctx, c, k2, false); err == nil || !strings.Contains(err.Error(), "无法解密 1 个供应商") || strings.Contains(err.Error(), "sk-secret") {
		t.Fatalf("mismatch: %v", err)
	}
	if err := gateway.SelfCheck(ctx, c, k1, true); err != nil {
		t.Fatal(err)
	}
}

func TestRotate(t *testing.T) {
	c := storetest.NewTestClient(t)
	ctx := context.Background()
	k1, k2 := kr(t, strings.Repeat("11", 32)), kr(t, strings.Repeat("22", 32))
	for _, n := range []string{"a", "b"} {
		enc, _ := k1.Seal(n, "sk-"+n)
		c.ModelProvider.Create().SetName(n).SetBaseURL("http://x").SetAPIKeyEnc(enc).ExecX(ctx)
	}
	// MCP 鉴权头同样随主密钥轮换（AAD 绑定服务器 ID）
	ms := c.McpServer.Create().SetOrgID(1).SetName("工单").SetURL("http://x/mcp").SetCreatedBy(1).SaveX(ctx)
	menc, _ := k1.SealAAD(gateway.MCPHeadersAAD(ms.ID), `{"Authorization":"Bearer t"}`)
	c.McpServer.UpdateOneID(ms.ID).SetHeadersEnc(menc).ExecX(ctx)
	snapshot := func() map[string][]byte {
		m := map[string][]byte{}
		for _, p := range c.ModelProvider.Query().AllX(ctx) {
			m[p.Name] = p.APIKeyEnc
		}
		return m
	}
	before := snapshot()
	// 审计失败：整体回滚
	_, err := gateway.Rotate(ctx, c, k1, k2, func(context.Context, *ent.Tx, []string) error { return errors.New("down") })
	if err == nil || !strings.Contains(err.Error(), "审计写入失败") {
		t.Fatalf("audit fail: %v", err)
	}
	for n, b := range snapshot() {
		if string(b) != string(before[n]) {
			t.Fatal("审计失败时不应修改密文")
		}
	}
	// 旧密钥不对：不做任何修改
	if _, err := gateway.Rotate(ctx, c, k2, k1, nil); err == nil {
		t.Fatal("wrong old key")
	}
	if _, err := gateway.Rotate(ctx, c, k1, k1, nil); err == nil {
		t.Fatal("same key")
	}
	var audited []string
	names, err := gateway.Rotate(ctx, c, k1, k2, func(_ context.Context, _ *ent.Tx, ps []string) error { audited = ps; return nil })
	if err != nil || len(names) != 3 || len(audited) != 3 || names[2] != "MCP:工单" {
		t.Fatalf("rotate %v %v", err, names)
	}
	for _, p := range c.ModelProvider.Query().AllX(ctx) {
		if v, err := k2.Open(p.Name, p.APIKeyEnc); err != nil || v != "sk-"+p.Name {
			t.Fatalf("new key open %s: %v", p.Name, err)
		}
		if _, err := k1.Open(p.Name, p.APIKeyEnc); err == nil {
			t.Fatal("old key should no longer open")
		}
	}
	m := c.McpServer.GetX(ctx, ms.ID)
	if v, err := k2.OpenAAD(gateway.MCPHeadersAAD(ms.ID), *m.HeadersEnc); err != nil || v != `{"Authorization":"Bearer t"}` {
		t.Fatalf("mcp headers after rotate: %v", err)
	}
	if _, err := k2.OpenAAD(gateway.MCPHeadersAAD(ms.ID+1), *m.HeadersEnc); err == nil {
		t.Fatal("AAD 应绑定服务器 ID")
	}
}
