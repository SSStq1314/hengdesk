package api

import "time"

// systemLoc 返回系统时区（D-37 / D-46：可配置，默认 Asia/Shanghai，不依赖容器 TZ）。
func systemLoc(loc *time.Location) *time.Location {
	if loc != nil {
		return loc
	}
	l, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.UTC
	}
	return l
}

// parseDateParam 解析 YYYY-MM-DD（按系统时区 0 点）或 RFC3339。空字符串返回零值。
func parseDateParam(v string, loc *time.Location) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02", v, systemLoc(loc))
}
