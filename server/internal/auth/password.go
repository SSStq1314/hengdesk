package auth

import (
	"bufio"
	_ "embed"
	"fmt"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

// 常见弱密码黑名单：SecLists Passwords/Common-Credentials/10k-most-common.txt（MIT 许可，
// 提交 68bcd12457978649d41ae4f56e3b5d2db2b72560），已转小写去重。出处见仓库 NOTICE。
//
//go:embed passwordlist/common.txt
var commonList string

var common = func() map[string]struct{} {
	m := make(map[string]struct{}, 10000)
	sc := bufio.NewScanner(strings.NewReader(commonList))
	for sc.Scan() {
		if w := strings.TrimSpace(sc.Text()); w != "" {
			m[w] = struct{}{}
		}
	}
	return m
}()

// DefaultMinPasswordLen 默认最小长度。
const DefaultMinPasswordLen = 8

// maxPasswordBytes bcrypt 只取前 72 字节。
const maxPasswordBytes = 72

// PasswordPolicy 本地密码策略。
type PasswordPolicy struct {
	MinLen int // 0 取默认 8
}

// Validate 校验密码：长度、常见弱密码（忽略大小写）、不得包含邮箱本地部分（忽略大小写）。
func (pp PasswordPolicy) Validate(pw, email string) error {
	minLen := pp.MinLen
	if minLen <= 0 {
		minLen = DefaultMinPasswordLen
	}
	if n := len([]rune(pw)); n < minLen {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("密码至少 %d 位", minLen))
	}
	if len(pw) > maxPasswordBytes {
		return echo.NewHTTPError(http.StatusBadRequest, "密码最长 72 字节")
	}
	low := strings.ToLower(pw)
	if _, bad := common[low]; bad {
		return echo.NewHTTPError(http.StatusBadRequest, "密码过于常见，请换一个")
	}
	if at := strings.IndexByte(email, '@'); at > 0 {
		local := strings.ToLower(email[:at])
		if len(local) >= 3 && strings.Contains(low, local) {
			return echo.NewHTTPError(http.StatusBadRequest, "密码不能包含邮箱用户名")
		}
	}
	return nil
}
