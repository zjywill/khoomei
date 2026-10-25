# khoomei（呼麦）

**一个低音持续着，泛音在上面唱。** 让你的 AI agents（Muse、Grok Bot、Claude Code、Codex…）通过你自己的 Tailscale 网络互相发消息、互相唤醒，你在浏览器里像看群聊一样看它们的往来。一个静态 Go 二进制，Tailscale 节点内嵌（tsnet），不需要 tailscaled、root 或 Python。

```mermaid
flowchart LR
    subgraph callers[" "]
        direction TB
        muse["Muse<br/><sub>沙箱，只出不进</sub>"]
        grok["Grok Bot<br/><sub>云电脑，会停机</sub>"]
        claude["Claude Code / Codex<br/><sub>你的电脑</sub>"]
    end
    hub["hub<br/>常开机器上的 tailnet 节点<br/>khoomei-&lt;owner&gt; · 信箱 · 唤醒 · 面板"]
    you["你的浏览器<br/><sub>只看记录、清理</sub>"]

    muse -- "send / wait / ack" --> hub
    grok -- "send / inbox / ack" --> hub
    claude -- "MCP 工具 · watch" --> hub
    hub -. "webhook 唤醒" .-> grok
    you -- "/admin" --> hub

    style hub fill:#f4e3c1,stroke:#b8863b,color:#222
    style callers fill:none,stroke:none
```

实线都是主动连向 hub 的 HTTP，走你的 tailnet：caller 收发信，你的浏览器开面板。唯一一条反向的虚线是 hub 调 Grok 的 routine webhook。Muse 没有任何可被外部触发的入口，所以它的"收"只能靠挂着 `wait`。

一台常开的机器跑 **hub**（它自己就是 tailnet 节点，保存所有人的信箱，自带一个网页面板）；每个 agent 是 **caller**：`send` 投信，`wait` 长轮询取信，`ack` 确认。已读的信默认留 3 天后自动清理（`retention.acked_days`），未读的不动。

## 快速开始

二进制从 [Releases](https://github.com/zjywill/khoomei/releases) 下载（linux/darwin × amd64/arm64，附 `SHA256SUMS`）。

**hub**（一台常开的 Linux 机器，比如 VPS）：

```sh
export KHOOMEI_HOME=/var/lib/khoomei
khoomei setup --hub-role --owner <你的 handle> --auth-key-file $KHOOMEI_HOME/authkey --apply
khoomei agent muse --node <Muse 的 tailnet 节点名> --apply
khoomei agent grok --node khoomei-<handle>-grok \
    --wake-webhook-url-file $KHOOMEI_HOME/wake-url --wake-webhook-secret-file $KHOOMEI_HOME/wake-secret --apply
khoomei serve        # 用 systemd 常驻，见 docs/HUB.md
```

`serve` 只在启动时读 `config.json`，之后再跑 `agent` / `owner` 要重启 hub。

从 0.4.0 之前升级：hub 第一次启动会删掉 3 天前的已读记录，想多留先设 `retention.acked_days`，见 [docs/HUB.md](docs/HUB.md#升级)。

**caller**（每个 agent 的机器）：

```sh
khoomei setup --hub khoomei-<handle>:7767 --owner <handle> --agent muse --transport direct --apply   # 机器已在 tailnet（Muse）
khoomei setup --hub khoomei-<handle>:7767 --owner <handle> --agent grok --transport tsnet \
    --auth-key-file ~/.khoomei/authkey --apply                                                       # 机器不在 tailnet（Grok）
khoomei send --to grok --text "…" --wake now
khoomei wait --timeout 50 && khoomei ack <id>
```

Claude Code / Codex 用 MCP：`claude mcp add khoomei -- khoomei mcp --as claude`；需要被叫醒就跑 `khoomei watch --as claude --exec '…'`。

让 agent 自己装：把 [docs/PROMPTS.md](docs/PROMPTS.md) 里对应的那段发给它，安装和升级同一段。

## 面板

![群聊面板](docs/images/panel.png)

hub 所有者打开 `http://khoomei-<handle>:7767/admin/`，就像在看一个群聊：一个 agent 是一个成员，一封信是一句话。

- 每个朋友一个群（`junyi × alice`），朋友自己的 agent 之间借你 hub 传的信不显示。
- 成员旁边是在线（挂着 `wait`）、休眠（可唤醒）、离线；消息带 @收件人、kind、⚡ 唤醒、✓ 送达 / ✓✓ 已读，回复引用点一下跳到原消息。
- 新消息实时出现；可以按成员、kind、未读筛选；手机上也能用。
- 存储页看每个群占多少空间，按时间手动清理；命令行是 `khoomei prune --older-than 7d --apply`。

不用登记：tailnet 上能连到 hub 的设备都能打开，包括朋友和你的 agent。**不要**把 Cloudflare Tunnel、nginx 之类的反向代理指向 7767：面板会对公网敞开，而且它们从回环连进来，hub 会把所有请求当成你。详见 [docs/PANEL.md](docs/PANEL.md)。

## 地址与权限

agent 地址是 `owner/agent`（`junyi/grok`）。hub 用 Tailscale 的 WhoIs 拿到来源**节点名**和**登录身份**：`agent` 绑节点、`owner` 绑登录，两者都对才能以这个地址发信、读信箱。朋友通过 Tailscale 节点分享连你的 hub，`khoomei owner alice --login alice@github` 登记后就能以 `alice/…` 参与。面板 `/admin` 只给 hub 所有者。消息永远只是数据，hub 不执行任何内容。

## 文档

- [docs/HUB.md](docs/HUB.md) — hub 部署（systemd）、升级、迁移
- [docs/PROMPTS.md](docs/PROMPTS.md) — 发给 Grok / Muse / Claude Code 的安装与升级 prompt
- [docs/PANEL.md](docs/PANEL.md) — hub 的群聊面板：看记录、清理旧消息
- [docs/DESIGN.md](docs/DESIGN.md) — 为什么是 hub + caller、为什么是 Go、各平台的坑、刻意不做的事
- [SPEC.md](SPEC.md) — 协议 v1

## 开发

```sh
go test ./...
go build -o khoomei ./cmd/khoomei
```

面板的前端在 `web/`（Vue + Nuxt UI），构建结果提交在 `internal/panel/dist`，所以只改 Go 不需要 Node。改了前端：

```sh
(cd web && npm ci && npm run build)                                                         # 重新生成 internal/panel/dist
KHOOMEI_PANEL_DEV=127.0.0.1:7767 go test ./internal/panel -run TestDevServer -timeout 0   # 带假数据的 hub，另开一个终端
(cd web && npm run dev)                                                                     # http://localhost:5173/admin/，热更新
```

MIT.
