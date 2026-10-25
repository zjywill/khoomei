//go:build !unix

package disk

func Space(string) (free, total uint64) { return 0, 0 }
