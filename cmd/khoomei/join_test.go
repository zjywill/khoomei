package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zjywill/khoomei/internal/config"
	"github.com/zjywill/khoomei/internal/hub"
	"github.com/zjywill/khoomei/internal/mailbox"
)

// A friend's caller knows only the hub's address; `khoomei join` gets it a handle and saves it.
func TestJoinCommand(t *testing.T) {
	hcfg, _ := config.Load(t.TempDir())
	hcfg.Role, hcfg.Owner = config.RoleHub, "junyi"
	h := hub.New(hcfg, mailbox.New(hcfg.Home), nil, t.Logf)
	h.Identify = func(*http.Request) (hub.Identity, error) {
		return hub.Identity{Node: "khoomei-hub-grok.tail7777.ts.net", Login: "carol@github"}, nil
	}
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()

	cfg, _ := config.Load(t.TempDir())
	cfg.Role, cfg.Hub, cfg.Agent, cfg.Transport = config.RoleCaller, strings.TrimPrefix(srv.URL, "http://"), "grok", config.TransportDirect
	if err := cmdJoin(context.Background(), cfg, nil); err != nil {
		t.Fatal(err)
	}
	back, _ := config.Load(cfg.Home)
	if back.Owner != "carol" || back.SelfAddress() != "carol/grok" {
		t.Fatalf("saved owner %q address %q", back.Owner, back.SelfAddress())
	}
	if h.Dir.Agent("carol/grok") == nil {
		t.Fatal("hub does not know carol/grok")
	}
	// Running it again is harmless.
	if err := cmdJoin(context.Background(), back, nil); err != nil {
		t.Fatalf("second join: %v", err)
	}
}

// With tsnet, the node name is pinned before the handle arrives, so it never changes afterwards.
func TestJoinPinsNodeName(t *testing.T) {
	cfg, _ := config.Load(t.TempDir())
	cfg.Role, cfg.Agent, cfg.Transport = config.RoleCaller, "grok", config.TransportTSNet
	if cfg.TSNetHostname() != "khoomei-hub-grok" {
		t.Fatalf("default name %s", cfg.TSNetHostname())
	}
	cfg.TSNet.Hostname = cfg.TSNetHostname() // what cmdJoin does before bringing the node up
	cfg.Owner = "carol"
	if cfg.TSNetHostname() != "khoomei-hub-grok" {
		t.Fatalf("renamed to %s", cfg.TSNetHostname())
	}
}
