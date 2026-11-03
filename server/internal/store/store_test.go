package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/ent/schema"
	"github.com/SSStq1314/hengdesk/server/internal/store"
	"github.com/SSStq1314/hengdesk/server/internal/store/storetest"
)

func TestSeedIdempotent(t *testing.T) {
	c := storetest.NewTestClient(t)
	ctx := context.Background()
	o := store.SeedOptions{OrgName: "默认组织", OrgSlug: "default", AdminEmail: "Admin@X.local", AdminPassword: "password123"}
	if ok, err := store.Seed(ctx, c, o); err != nil || !ok {
		t.Fatalf("first seed: %v %v", ok, err)
	}
	if ok, err := store.Seed(ctx, c, o); err != nil || ok {
		t.Fatalf("second seed: %v %v", ok, err)
	}
	if n := c.Role.Query().CountX(ctx); n != len(store.BuiltinRoles) {
		t.Fatalf("roles=%d", n)
	}
}

func TestSeedRequiresPassword(t *testing.T) {
	c := storetest.NewTestClient(t)
	if _, err := store.Seed(context.Background(), c, store.SeedOptions{OrgName: "o", OrgSlug: "o", AdminEmail: "a@b.c"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestAuditAppendOnly(t *testing.T) {
	c := storetest.NewTestClient(t)
	ctx := context.Background()
	a := c.AuditLog.Create().SetAction("test").SaveX(ctx)
	if err := c.AuditLog.UpdateOne(a).Exec(ctx); !errors.Is(err, schema.ErrAuditImmutable) {
		t.Fatalf("update should be rejected, got %v", err)
	}
	if _, err := c.AuditLog.Delete().Exec(ctx); !errors.Is(err, schema.ErrAuditImmutable) {
		t.Fatalf("delete should be rejected, got %v", err)
	}
}

func TestNewToolDisabledByDefault(t *testing.T) {
	c := storetest.NewTestClient(t)
	tool := c.Tool.Create().SetKey("t").SetName("t").SaveX(context.Background())
	if tool.Enabled || tool.ExecMode != "require_approval" {
		t.Fatalf("tool defaults wrong: %+v", tool)
	}
}
