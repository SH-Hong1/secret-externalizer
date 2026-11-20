//go:build unix

package rewrite

import (
	"io/fs"
	"syscall"
)

// singleLink reports whether info is a file with exactly one hard link. A
// file system that gives no link count counts as more than one.
func singleLink(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink == 1
}
