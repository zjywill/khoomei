package panel

import (
	"context"
	"fmt"
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
func TestDevServer(t *testing.T) {
	addr := os.Getenv("KHOOMEI_PANEL_DEV")
	if addr == "" {
		t.Skip("set KHOOMEI_PANEL_DEV=HOST:PORT to run the panel dev server")
	}
	cfg, _ := config.Load(t.TempDir())
	cfg.Role, cfg.Owner = config.RoleHub, "junyi"
	cfg.Owners = map[string]*config.OwnerSpec{"alice": {Login: aliceLogin}}
	cfg.Agents = map[string]*config.AgentSpec{
		"junyi/grok":   {Node: "khoomei-junyi-grok", Wake: &config.WakeAdapter{Type: "exec", Argv: []string{"true"}}},
		"junyi/muse":   {Node: "muse-vm"},
		"junyi/claude": {Local: true},
		"junyi/codex":  {Local: true, Wake: &config.WakeAdapter{Type: "exec", Argv: []string{"false"}}},
		"alice/muse":   {Node: aliceNode},
		"alice/codex":  {Node: aliceNode},
	}
	box := mailbox.New(cfg.Home)
	h := hub.New(cfg, box, nil, t.Logf)
	p, err := New(cfg, box, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	h.Observer, h.Admin = p, p.Handler()
	h.Identify = func(r *http.Request) (hub.Identity, error) { return hub.Identity{Local: true}, nil }

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
	send("alice/muse", "alice/codex", "text", "这条是 alice 自己的，不该出现在面板里", "", "")
	send("junyi/claude", "junyi/grok", "text", "被清理过的回复引用", "Zz0000000000pruned", "")

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
