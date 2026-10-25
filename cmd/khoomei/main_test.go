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
