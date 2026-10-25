package client

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestCleanFetched(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-4 * 24 * time.Hour)
	for _, d := range []string{"OldMessageId1", "NewMessageId2", "my notes"} {
		os.MkdirAll(filepath.Join(root, d), 0o700)
		os.WriteFile(filepath.Join(root, d, "f"), []byte("x"), 0o600)
		if d != "NewMessageId2" {
			os.Chtimes(filepath.Join(root, d), old, old)
		}
	}
	if n := CleanFetched(root, KeepFetched); n != 1 {
		t.Fatalf("removed %d", n)
	}
	for d, want := range map[string]bool{"OldMessageId1": false, "NewMessageId2": true, "my notes": true} {
		if _, err := os.Stat(filepath.Join(root, d)); (err == nil) != want {
			t.Errorf("%s: exists=%v", d, err == nil)
		}
	}
}
