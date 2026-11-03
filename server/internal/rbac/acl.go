package rbac

// ACL 权限位。高位蕴含低位：manage ⊇ edit ⊇ use ⊇ view。
const (
	BitView   = 1 << 0
	BitUse    = 1 << 1
	BitEdit   = 1 << 2
	BitManage = 1 << 3
)

// Normalize 补齐蕴含的低位，并丢弃未知位。
func Normalize(bits int) int {
	bits &= BitView | BitUse | BitEdit | BitManage
	switch {
	case bits&BitManage != 0:
		return BitManage | BitEdit | BitUse | BitView
	case bits&BitEdit != 0:
		return BitEdit | BitUse | BitView
	case bits&BitUse != 0:
		return BitUse | BitView
	default:
		return bits
	}
}

// BitNames 权限位名称。
var BitNames = map[string]int{"view": BitView, "use": BitUse, "edit": BitEdit, "manage": BitManage}

// ParseBits 由名称列表得到权限位。
func ParseBits(names []string) (int, bool) {
	b := 0
	for _, n := range names {
		v, ok := BitNames[n]
		if !ok {
			return 0, false
		}
		b |= v
	}
	return Normalize(b), true
}

// BitsToNames 权限位转名称（从低到高）。
func BitsToNames(b int) []string {
	out := []string{}
	for _, n := range []string{"view", "use", "edit", "manage"} {
		if b&BitNames[n] != 0 {
			out = append(out, n)
		}
	}
	return out
}

// 资源类型。新资源类型复用同一 ACL 层（知识库文件 ACL 在知识库阶段接入）。
const (
	ResKB    = "kb"
	ResAgent = "agent"
	ResTool  = "tool"
)
