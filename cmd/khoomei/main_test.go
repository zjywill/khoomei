package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zjywill/khoomei/internal/config"
)

// "agent NAME --flags" and "agent --flags NAME" must both work.
func TestAgentNameBeforeFlags(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{
		{"--home", home, "setup", "--hub-role", "--owner", "junyi", "--apply"},
		{"--home", home, "agent", "muse", "--node", "muse-vm", "--apply"},
		{"--home", home, "agent", "--node", "laptop", "--apply", "codex"},
		{"--home", home, "owner", "alice", "--login", "alice@github", "--apply"},
	} {
		if err := run(args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	cfg, _ := config.Load(home)
	if cfg.Agents["junyi/muse"] == nil || cfg.Agents["junyi/muse"].Node != "muse-vm" {
		t.Fatalf("muse not registered: %+v", cfg.Agents)
	}
	if cfg.Agents["junyi/codex"] == nil || cfg.Agents["junyi/codex"].Node != "laptop" {
		t.Fatalf("codex not registered: %+v", cfg.Agents)
	}
	if cfg.Owners["alice"] == nil || cfg.Owners["alice"].Login != "alice@github" {
		t.Fatalf("alice not registered: %+v", cfg.Owners)
	}
	b, _ := os.ReadFile(filepath.Join(home, "config.json"))
	if !strings.Contains(string(b), `"junyi/muse"`) {
		t.Fatal("config not saved")
	}
}

func TestAdminCommand(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{
		{"--home", home, "setup", "--hub-role", "--owner", "junyi", "--apply"},
		{"--home", home, "admin", "junyi-mbp", "--login", "Junyi@GitHub", "--apply"},
		{"--home", home, "admin", "junyi-mbp", "--login", "junyi@github", "--apply"}, // replaces, no duplicate
		{"--home", home, "admin", "--login", "junyi@github", "--apply", "ipad"},
	} {
		if err := run(args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if err := run([]string{"--home", home, "admin", "phone", "--apply"}); err == nil {
		t.Fatal("admin without --login accepted")
	}
	cfg, _ := config.Load(home)
	if len(cfg.Admins) != 2 || cfg.Admins[0] != (config.AdminSpec{Node: "junyi-mbp", Login: "junyi@github"}) {
		t.Fatalf("admins: %+v", cfg.Admins)
	}
	if err := run([]string{"--home", home, "admin", "junyi-mbp", "--remove", "--apply"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.Load(home)
	if len(cfg.Admins) != 1 || cfg.Admins[0].Node != "ipad" {
		t.Fatalf("after remove: %+v", cfg.Admins)
	}
}

func TestParseAge(t *testing.T) {
	for in, want := range map[string]int{"24h": 24, "36h": 36, "7d": 168, "0d": 0, "90m": 1} {
		if got, err := parseAge(in); err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "7", "-1d", "xd", "-2h"} {
		if _, err := parseAge(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
