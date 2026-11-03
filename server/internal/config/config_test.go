package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	t.Setenv("HENGDESK_HTTP_ADDR", ":9999")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":9999" || c.Env != "dev" {
		t.Fatalf("unexpected %+v", c)
	}
}

func TestProdRequiresSecret(t *testing.T) {
	t.Setenv("HENGDESK_ENV", "prod")
	t.Setenv("HENGDESK_JWT_SECRET", "short")
	if _, err := Load(); err == nil {
		t.Fatal("expected error")
	}
}
