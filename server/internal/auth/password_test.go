package auth

import "testing"

func TestPasswordPolicy(t *testing.T) {
	if len(common) < 9900 {
		t.Fatalf("blacklist too small: %d", len(common))
	}
	pp := PasswordPolicy{}
	cases := []struct {
		pw, email string
		ok        bool
	}{
		{"short", "a@x.io", false},
		{"password", "a@x.io", false},             // 黑名单
		{"PassWord", "a@x.io", false},             // 黑名单忽略大小写
		{"QWERTYUIOP", "a@x.io", false},           // 黑名单忽略大小写
		{"zhangsan#2026", "ZhangSan@x.io", false}, // 含邮箱本地部分（忽略大小写）
		{"Tq9#vLx2pM", "zhangsan@x.io", true},
		{"ab-Tq9#vLx2", "ab@x.io", true}, // 本地部分少于 3 个字符不检查
	}
	for _, c := range cases {
		if err := pp.Validate(c.pw, c.email); (err == nil) != c.ok {
			t.Errorf("%q/%q: ok=%v err=%v", c.pw, c.email, c.ok, err)
		}
	}
	if err := (PasswordPolicy{MinLen: 12}).Validate("Tq9#vLx2pM", "a@x.io"); err == nil {
		t.Error("min length 12 not enforced")
	}
}
