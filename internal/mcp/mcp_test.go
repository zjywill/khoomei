package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zjywill/khoomei/internal/client"
	"github.com/zjywill/khoomei/internal/config"
	"github.com/zjywill/khoomei/internal/hub"
	"github.com/zjywill/khoomei/internal/mailbox"
	"github.com/zjywill/khoomei/internal/protocol"
)

// A hub where loopback acts as the owner, with claude and grok both local.
func newHub(t *testing.T) *httptest.Server {
	cfg, _ := config.Load(t.TempDir())
	cfg.Role = config.RoleHub
	cfg.Agents = map[string]*config.AgentSpec{"grok": {Local: true}, "claude": {Local: true}}
	h := hub.New(cfg, mailbox.New(cfg.Home), nil, t.Logf)
	h.Identify = func(*http.Request) (hub.Identity, error) { return hub.Identity{Local: true}, nil }
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func session(t *testing.T, srv *httptest.Server, lines ...string) []map[string]any {
	t.Helper()
	return sessionAs(t, srv, "claude", "", lines...)
}

func sessionAs(t *testing.T, srv *httptest.Server, agent, filesDir string, lines ...string) []map[string]any {
	t.Helper()
	s := &Server{Client: client.New(srv.URL, nil), Agent: agent, Version: "test", FilesDir: filesDir}
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var replies []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad reply line %q: %v", l, err)
		}
		replies = append(replies, m)
	}
	return replies
}

func toolText(t *testing.T, reply map[string]any) string {
	t.Helper()
	res, ok := reply["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in %v", reply)
	}
	content := res["content"].([]any)
	return content[0].(map[string]any)["text"].(string)
}

func TestHandshakeAndTools(t *testing.T) {
	srv := newHub(t)
	replies := session(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"nope"}`,
	)
	if len(replies) != 3 {
		t.Fatalf("want 3 replies (notification unanswered), got %d", len(replies))
	}
	init := replies[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-03-26" {
		t.Errorf("should echo the client's protocol version: %v", init["protocolVersion"])
	}
	tools := replies[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 5 {
		t.Errorf("want 5 tools, got %d", len(tools))
	}
	if replies[2]["error"] == nil {
		t.Error("unknown method should be an error")
	}
}

func TestSendInboxAck(t *testing.T) {
	srv := newHub(t)
	// grok leaves a message for claude first.
	owner := client.New(srv.URL, nil)
	owner.Send(context.Background(), protocol.New("grok", "claude", "你好 claude"))

	replies := session(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"khoomei_inbox","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"khoomei_send","arguments":{"to":"grok","text":"收到","wake":"now","data":{"ok":true}}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"khoomei_send","arguments":{"to":"nobody","text":"x"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"khoomei_wait","arguments":{"timeout":1}}}`,
	)
	var inbox client.Response
	json.Unmarshal([]byte(toolText(t, replies[0])), &inbox)
	if len(inbox.Messages) != 1 || inbox.Messages[0].Envelope.Text != "你好 claude" {
		t.Fatalf("inbox: %+v", inbox)
	}
	var sent client.Response
	json.Unmarshal([]byte(toolText(t, replies[1])), &sent)
	if sent.Status != 202 || sent.ID == "" {
		t.Fatalf("send: %+v", sent)
	}
	if res := replies[2]["result"].(map[string]any); res["isError"] != true {
		t.Errorf("send to unknown agent should be a tool error: %v", res)
	}
	// grok's inbox got it with from_agent=claude and the data intact.
	got, _ := owner.Inbox(context.Background(), "grok")
	if len(got.Messages) != 1 || got.Messages[0].Envelope.FromAgent != "claude" || string(got.Messages[0].Envelope.Data) != `{"ok":true}` {
		t.Fatalf("grok inbox: %+v", got.Messages)
	}

	// ack through the tool, then the inbox is empty.
	replies = session(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"khoomei_ack","arguments":{"ids":["`+inbox.Messages[0].Envelope.ID+`"]}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"khoomei_inbox","arguments":{}}}`,
	)
	var acked client.Response
	json.Unmarshal([]byte(toolText(t, replies[0])), &acked)
	if len(acked.Acked) != 1 {
		t.Fatalf("ack: %+v", acked)
	}
	var after client.Response
	json.Unmarshal([]byte(toolText(t, replies[1])), &after)
	if len(after.Messages) != 0 {
		t.Fatalf("inbox after ack: %+v", after.Messages)
	}
}

func TestSendAndFetchFiles(t *testing.T) {
	srv := newHub(t)
	src := filepath.Join(t.TempDir(), "notes.md")
	os.WriteFile(src, []byte("# 会议纪要\n"), 0o600)
	args, _ := json.Marshal(map[string]any{"to": "grok", "text": "附件", "files": []string{src}})
	replies := session(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"khoomei_send","arguments":`+string(args)+`}}`)
	var sent client.Response
	json.Unmarshal([]byte(toolText(t, replies[0])), &sent)
	if sent.Status != 202 {
		t.Fatalf("send with file: %s", toolText(t, replies[0]))
	}

	dir := t.TempDir()
	replies = sessionAs(t, srv, "grok", dir,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"khoomei_fetch","arguments":{"id":"`+sent.ID+`"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"khoomei_fetch","arguments":{"id":"nosuchmessage"}}}`,
	)
	var got struct{ Files []string }
	json.Unmarshal([]byte(toolText(t, replies[0])), &got)
	if len(got.Files) != 1 || filepath.Base(got.Files[0]) != "notes.md" || !strings.HasPrefix(got.Files[0], dir) {
		t.Fatalf("fetch: %s", toolText(t, replies[0]))
	}
	if b, _ := os.ReadFile(got.Files[0]); string(b) != "# 会议纪要\n" {
		t.Fatalf("content: %q", b)
	}
	if res := replies[1]["result"].(map[string]any); res["isError"] != true {
		t.Errorf("fetch of an unknown message should be a tool error: %v", res)
	}
}
