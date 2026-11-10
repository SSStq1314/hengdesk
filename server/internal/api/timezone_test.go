package api

import (
	"testing"
	"time"
)

func TestParseDateParamTimezone(t *testing.T) {
	sh, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseDateParam("2026-11-10", sh)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 10, 0, 0, 0, 0, sh)
	if !got.Equal(want) {
		t.Fatalf("shanghai midnight %v want %v", got, want)
	}
	utcGot, err := parseDateParam("2026-11-10", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if !utcGot.Equal(time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("utc %v", utcGot)
	}
	if utcGot.Equal(got) {
		t.Fatal("UTC 与 Asia/Shanghai 的同一日历日 0 点必须不是同一瞬间")
	}
	// 未配置时区时默认 Asia/Shanghai
	def, err := parseDateParam("2026-11-10", nil)
	if err != nil || !def.Equal(want) {
		t.Fatalf("default loc %v err=%v", def, err)
	}
}
