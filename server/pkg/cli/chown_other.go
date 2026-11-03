//go:build !unix

package cli

import "os"

func chownLike(os.FileInfo, ...string) {}
