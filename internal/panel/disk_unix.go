//go:build unix

package panel

import "golang.org/x/sys/unix"

// diskSpace reports free and total bytes of the filesystem holding dir; zeros when unknown.
func diskSpace(dir string) (free, total uint64) {
	var st unix.Statfs_t
	if unix.Statfs(dir, &st) != nil {
		return 0, 0
	}
	return st.Bavail * uint64(st.Bsize), st.Blocks * uint64(st.Bsize)
}
