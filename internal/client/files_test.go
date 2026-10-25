package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"report.pdf":       "report.pdf",
		"../../etc/passwd": "_.._etc_passwd",
		"..":               "file",
		".bashrc":          "bashrc",
		"a\\b:c\nd":        "a_b_c_d",
		"   ":              "file",
		"会议纪要 v2.md":       "会议纪要 v2.md",
	} {
		if got := SafeName(in); got != want {
			t.Errorf("SafeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), nil, 0o600)
	os.WriteFile(filepath.Join(dir, "a (2).txt"), nil, 0o600)
	if got := uniquePath(dir, "a.txt"); filepath.Base(got) != "a (3).txt" {
		t.Fatalf("got %s", got)
	}
	if got := uniquePath(dir, "b.txt"); filepath.Base(got) != "b.txt" {
		t.Fatalf("got %s", got)
	}
}
