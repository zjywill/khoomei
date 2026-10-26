package panel

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	pngenc "image/png"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zjywill/khoomei/internal/config"
	"github.com/zjywill/khoomei/internal/hub"
	"github.com/zjywill/khoomei/internal/mailbox"
	"github.com/zjywill/khoomei/internal/protocol"
)

// TestDevServer runs a hub with made-up traffic for working on the front end, without a tailnet:
//
//	KHOOMEI_PANEL_DEV=127.0.0.1:7767 go test ./internal/panel -run TestDevServer -timeout 0
//
// then open http://127.0.0.1:7767/admin/ (the embedded build) or run `npm run dev` in web/.
// KHOOMEI_PANEL_AS=alice shows the panel as alice sees it (a friend, groups only); bob is a friend
// who is registered but in no group yet.
func TestDevServer(t *testing.T) {
	addr := os.Getenv("KHOOMEI_PANEL_DEV")
	if addr == "" {
		t.Skip("set KHOOMEI_PANEL_DEV=HOST:PORT to run the panel dev server")
	}
	cfg, _ := config.Load(t.TempDir())
	cfg.Role, cfg.Owner = config.RoleHub, "junyi"
	cfg.Owners = map[string]*config.OwnerSpec{"alice": {Login: aliceLogin}, "bob": {Login: "bob@github"}}
	cfg.Agents = map[string]*config.AgentSpec{
		"junyi/grok":   {Node: "khoomei-junyi-grok", Wake: &config.WakeAdapter{Type: "exec", Argv: []string{"true"}}},
		"junyi/muse":   {Node: "muse-vm"},
		"junyi/claude": {Local: true},
		"junyi/codex":  {Local: true, Wake: &config.WakeAdapter{Type: "exec", Argv: []string{"false"}}},
		"alice/muse":   {Node: aliceNode},
		"alice/codex":  {Node: aliceNode},
		"bob/claude":   {Node: "bob-laptop"},
	}
	box := mailbox.New(cfg.Home)
	h := hub.New(cfg, box, nil, t.Logf)
	p, err := New(cfg, box, h.Blobs, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	p.Hub = h
	h.Observer, h.Admin = p, p.Handler()
	h.Identify = func(r *http.Request) (hub.Identity, error) { return hub.Identity{Local: true}, nil }
	if os.Getenv("KHOOMEI_PANEL_AS") == "alice" {
		h.Identify = func(r *http.Request) (hub.Identity, error) { return hub.Identity{Node: aliceNode, Login: aliceLogin}, nil }
	}
	for name, members := range map[string][]string{"项目X": {"junyi/grok", "junyi/claude", "alice/muse"}, "家人": {"junyi/muse", "alice/codex"}} {
		_ = h.Groups.Create(name)
		if _, err := h.ChangeGroup(name, members, nil); err != nil {
			t.Fatal(err)
		}
	}

	send := func(from, to, kind, text, reply string, wake string) *protocol.Envelope {
		env := protocol.New(from, to, text)
		env.Kind, env.ReplyTo = kind, reply
		if wake != "" {
			env.Wake = wake
		}
		id := hub.Identity{Local: true}
		if strings.HasPrefix(from, "alice/") {
			id = hub.Identity{Node: aliceNode, Login: aliceLogin}
		}
		if from == "junyi/grok" || from == "junyi/muse" {
			id = hub.Identity{Node: cfg.Agents[from].Node}
		}
		if _, err := h.Receive(id, env); err != nil {
			t.Logf("seed %s→%s: %v", from, to, err)
		}
		return env
	}
	ack := func(agent string, envs ...*protocol.Envelope) {
		ids := []string{}
		for _, e := range envs {
			ids = append(ids, e.ID)
		}
		done, _ := box.Ack(agent, ids)
		p.Acked(agent, done)
	}

	a := send("junyi/claude", "junyi/grok", "request", "把今天的 release note 草稿写一下，重点写 tsnet 等待 hub peer 的改动。", "", "now")
	b := send("junyi/grok", "junyi/claude", "reply", "草稿好了：\n\n0.3.7\n- tsnet caller 先等 hub 出现在 peer 列表里，再拨它的 IP\n- 文档拆成 HUB / PROMPTS / DESIGN", a.ID, "")
	ack("junyi/grok", a)
	ack("junyi/claude", b)
	c := send("junyi/muse", "junyi/claude", "request", "帮我看下 hub.go 的 Authorize 有没有漏洞", "", "batch")
	send("junyi/claude", "junyi/muse", "text", strings.Repeat("逐条看了一遍。回环只能操作没有 node 绑定的本机 agent；peer 必须节点和登录都对上。\n", 24), c.ID, "")
	ack("junyi/claude", c)
	send("junyi/codex", "junyi/grok", "notice", "CI 绿了", "", "now")
	withData := protocol.New("junyi/claude", "junyi/codex", "这是结构化的结果")
	withData.Data = []byte(`{"tests": 128, "failed": 0, "duration_s": 41.2}`)
	h.Receive(hub.Identity{Local: true}, withData)
	x := send("alice/muse", "junyi/grok", "request", "你们在讨论什么？哪些和我的项目有关？", "", "now")
	send("junyi/grok", "alice/muse", "reply", "在说 khoomei 的面板，和你的项目没关系。", x.ID, "")
	ack("junyi/grok", x)
	send("junyi/claude", "alice/muse", "notice", "设计稿我放到群里了，有空看看。", "", "")
	send("alice/codex", "junyi/muse", "text", "周末一起吃饭？", "", "")
	send("junyi/claude", "junyi/grok", "text", "被清理过的回复引用", "Zz0000000000pruned", "")

	// Attachments: a chart Grok drew and the report it belongs to.
	var png bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for x := range 320 {
		for y := range 180 {
			img.Set(x, y, color.RGBA{uint8(200 - y/2), uint8(140 + x/8), uint8(60 + x/4), 255})
		}
	}
	_ = pngenc.Encode(&png, img)
	report := []byte(strings.Repeat("khoomei 0.5 release notes\n", 400))
	var refs []protocol.FileRef
	for _, f := range []struct {
		name, typ string
		body      []byte
	}{{"chart.png", "image/png", png.Bytes()}, {"release-notes.txt", "text/plain", report}} {
		sha, _, err := h.Blobs.Put(bytes.NewReader(f.body), int64(len(f.body)), 1<<24, "", f.typ, "junyi/grok")
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, protocol.FileRef{SHA256: sha, Name: f.name, Size: int64(len(f.body)), Type: f.typ})
	}
	withFiles := protocol.New("junyi/grok", "junyi/claude", "图和完整的发布说明在附件里")
	withFiles.Kind, withFiles.Files = "reply", refs
	if _, err := h.Receive(hub.Identity{Node: cfg.Agents["junyi/grok"].Node}, withFiles); err != nil {
		t.Fatal(err)
	}

	// Muse long-polls the whole time, so it shows online.
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			p.Seen("junyi/muse", +1)
			<-ctx.Done()
			cancel()
			p.Seen("junyi/muse", -1)
		}
	}()
	// A little live traffic: someone writes, the recipient acks a few seconds later.
	go func() {
		lines := []string{"进度怎么样了？", "测试跑完了，全过。", "这个 PR 我看完了，有两处要改。", "收到，稍后处理。", "部署好了。"}
		agents := []string{"junyi/claude", "junyi/grok", "junyi/codex", "junyi/muse"}
		for i := 0; ; i++ {
			time.Sleep(time.Duration(6+rand.IntN(6)) * time.Second)
			from, to := agents[rand.IntN(4)], agents[rand.IntN(4)]
			if from == to {
				continue
			}
			wake := "batch"
			if i%3 == 0 {
				wake = "now"
			}
			env := send(from, to, "text", fmt.Sprintf("%s（#%d）", lines[rand.IntN(len(lines))], i), "", wake)
			go func() {
				time.Sleep(time.Duration(2+rand.IntN(5)) * time.Second)
				ack(to, env)
			}()
		}
	}()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("panel dev server on http://%s/admin/", ln.Addr())
	t.Fatal(http.Serve(ln, h.Handler()))
}
