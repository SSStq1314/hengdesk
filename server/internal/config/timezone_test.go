package config

import "testing"

func TestLoadTimezone(t *testing.T) {
	for in, want := range map[string]string{"": "Asia/Shanghai", "Asia/Shanghai": "Asia/Shanghai", "UTC": "UTC"} {
		l, err := LoadTimezone(in)
		if err != nil || l.String() != want {
			t.Fatalf("%q → %v %v", in, l, err)
		}
	}
	if _, err := LoadTimezone("Mars/Base"); err == nil {
		t.Fatal("无效时区应报错")
	}
	t.Setenv("HENGDESK_TIMEZONE", "")
	if c, err := Load(); err == nil && c.Timezone.String() != "Asia/Shanghai" {
		t.Fatalf("默认时区 %s", c.Timezone)
	}
}
