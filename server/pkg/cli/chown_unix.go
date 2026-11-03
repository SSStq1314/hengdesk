//go:build unix

package cli

import (
	"os"
	"syscall"
)

func chownLike(st os.FileInfo, files ...string) {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		for _, f := range files {
			_ = os.Chown(f, int(s.Uid), int(s.Gid))
		}
	}
}
