package config

import (
	"testing"
)

func TestOwnerQualifiesAgentKeys(t *testing.T) {
	c, _ := Load(t.TempDir())
	c.Role, c.Owner, c.Agent = RoleHub, "junyi", "grok"
	c.Agents["grok"] = &AgentSpec{Local: true}
	c.Agents["alice/muse"] = &AgentSpec{Node: "alice-muse"}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	back, err := Load(c.Home)
	if err != nil {
		t.Fatal(err)
	}
	if back.Agents["junyi/grok"] == nil || back.Agents["grok"] != nil {
		t.Fatalf("bare key not qualified on save: %v", keys(back.Agents))
	}
	if back.Agents["alice/muse"] == nil {
		t.Fatal("foreign owner key lost")
	}
	if back.SelfAddress() != "junyi/grok" || back.TSNetHostname() != "khoomei-junyi" {
		t.Fatalf("self=%s host=%s", back.SelfAddress(), back.TSNetHostname())
	}
	if s, ok := back.Lookup("grok"); !ok || !s.Local {
		t.Fatal("Lookup by bare name failed")
	}
}

func TestNoOwnerKeepsBareNames(t *testing.T) {
	c, _ := Load(t.TempDir())
	c.Agent = "muse"
	c.Agents["grok"] = &AgentSpec{Local: true}
	c.Save()
	back, _ := Load(c.Home)
	if back.Agents["grok"] == nil || back.SelfAddress() != "muse" || back.TSNetHostname() != "khoomei-hub-muse" {
		t.Fatalf("single-owner mode changed: %v %s %s", keys(back.Agents), back.SelfAddress(), back.TSNetHostname())
	}
}

func keys(m map[string]*AgentSpec) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}
