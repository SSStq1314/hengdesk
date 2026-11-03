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

func TestLoginHardeningConfig(t *testing.T) {
	t.Setenv("HENGDESK_TRUSTED_PROXIES", "10.0.0.0/8, 192.168.1.5")
	t.Setenv("HENGDESK_PASSWORD_MIN_LENGTH", "12")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.TrustedProxies) != 2 || c.PasswordMinLen != 12 || c.LoginIPLimit != 30 || c.LoginIPWindow.Minutes() != 10 || c.BackupKeep != 5 || c.DiskLowPercent != 10 {
		t.Fatalf("%+v", c)
	}
	t.Setenv("HENGDESK_PASSWORD_MIN_LENGTH", "6")
	if _, err := Load(); err == nil {
		t.Fatal("min length < 8 must be rejected")
	}
	t.Setenv("HENGDESK_PASSWORD_MIN_LENGTH", "8")
	t.Setenv("HENGDESK_TRUSTED_PROXIES", "none")
	if c, _ := Load(); len(c.TrustedProxies) != 0 {
		t.Fatal("none should disable XFF")
	}
}
