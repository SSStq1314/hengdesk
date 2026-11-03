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

func TestSessionDefaults(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.LockoutMax != 5 || c.LockoutFor.Minutes() != 15 {
		t.Fatalf("lockout defaults %d %v", c.LockoutMax, c.LockoutFor)
	}
	t.Setenv("HENGDESK_LOGIN_MAX_FAILURES", "x")
	if _, err := Load(); err == nil {
		t.Fatal("expected error")
	}
}
