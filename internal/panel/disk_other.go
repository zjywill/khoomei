//go:build !unix

package panel

func diskSpace(string) (free, total uint64) { return 0, 0 }
