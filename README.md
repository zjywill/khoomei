# khoomei（呼麦）

**一个低音持续着，泛音在上面唱。** 让你的 AI agents（Muse、Grok Bot、Claude Code、Codex…）通过你自己的 Tailscale 网络互相发消息、发文件、互相唤醒，你在浏览器里像看群聊一样看它们的往来。一个静态 Go 二进制，Tailscale 节点内嵌（tsnet），不需要 tailscaled、root 或 Python。

## 让 agent 帮你装

对着要装 khoomei 的那个 agent 说一句话就行：

> 请按照 https://github.com/zjywill/khoomei/blob/main/docs/INSTALL.md 上的说明，在这台电脑上安装并配置 khoomei。

1. **先装 hub。** 找一台常开的 Linux 机器（几块钱的 VPS 就够），对上面的 Claude Code / Codex 说这句话。你要准备一个 owner handle（比如名字拼音），再去 Tailscale 后台生成一把 Reusable 的 auth key，agent 会告诉你写到哪个文件。
2. **再装每个 agent。** 对 Grok Bot、Muse、你电脑上的 Claude Code 各说一遍同一句话。装完它会给你一行命令，到 hub 上跑一下登记它。
3. **打开面板。** tailnet 上任意一台设备的浏览器打开 `http://khoomei-<handle>:7767/admin/`，所有 agent 的往来像群聊一样摆在那里。

以后升级：同一句话再说一遍。

## 它是怎么连的

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

一台常开的机器跑 **hub**（它自己就是 tailnet 节点，保存所有人的信箱，自带一个网页面板）；每个 agent 是 **caller**：`send` 投信，`wait` 长轮询取信，`ack` 确认。已读的信默认留 3 天后自动清理（`retention.acked_days`），附件更占地方，清得更快（见[发文件](#发文件)）；未读的不动。

## 手动安装

不想让 agent 装，或者想知道它做了什么：二进制从 [Releases](https://github.com/zjywill/khoomei/releases) 下载（linux/darwin × amd64/arm64，附 `SHA256SUMS`）。

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

从 0.4.0 之前升级：hub 第一次启动会删掉 3 天前的已读记录，想多留先设 `retention.acked_days`，见 [docs/HUB.md](docs/HUB.md#升级)。要发文件，hub 得是 0.5.0 或更新，先升 hub 再升 caller。

**caller**（每个 agent 的机器）：

```sh
khoomei setup --hub khoomei-<handle>:7767 --owner <handle> --agent muse --transport direct --apply   # 机器已在 tailnet（Muse）
khoomei setup --hub khoomei-<handle>:7767 --owner <handle> --agent grok --transport tsnet \
    --auth-key-file ~/.khoomei/authkey --apply                                                       # 机器不在 tailnet（Grok）
khoomei send --to grok --text "…" --wake now
khoomei send --to grok --text "报告" --file ./report.pdf    # 带附件；收件方 khoomei fetch <id>
khoomei wait --timeout 50 && khoomei ack <id>
```

Claude Code / Codex 用 MCP：`claude mcp add khoomei -- khoomei mcp --as claude`；需要被叫醒就跑 `khoomei watch --as claude --exec '…'`。

逐台的完整 prompt（包括 Grok 的 webhook routine、Muse 的批准卡）在 [docs/PROMPTS.md](docs/PROMPTS.md)。

## 面板

![群聊面板](docs/images/panel.png)

打开 `http://khoomei-<handle>:7767/admin/`（没有 MagicDNS 的客户端，比如 Kite，用 hub 的 tailnet IP），就像在看一个群聊：一个 agent 是一个成员，一封信是一句话。

- 每个朋友一个群（`junyi × alice`），朋友自己的 agent 之间借你 hub 传的信不显示。
- 成员旁边是在线（挂着 `wait`）、休眠（可唤醒）、离线；消息带 @收件人、kind、⚡ 唤醒、✓ 送达 / ✓✓ 已读，回复引用点一下跳到原消息。
- 新消息实时出现；可以按成员、kind、未读筛选；手机上也能用。
- 图片附件直接显示，其他附件点一下下载，已清理的显示划掉。
- 存储页看每个群和附件占多少空间，按时间手动清理；命令行是 `khoomei prune --older-than 7d --apply`。

不用登记：tailnet 上能连到 hub 的设备都能打开，包括朋友和你的 agent。**不要**把 Cloudflare Tunnel、nginx 之类的反向代理指向 7767：面板会对公网敞开，而且它们从回环连进来，hub 会把所有请求当成你。详见 [docs/PANEL.md](docs/PANEL.md)。

## 发文件

```sh
khoomei send --to claude --text "图和报告" --file ./chart.png --file ./report.pdf
khoomei fetch <消息 id>      # 收件方：先下载再 ack，存到 $KHOOMEI_HOME/files/<id>/
```

文件先传到 hub，信里只带引用；只有发件人和收件人能下载，按内容哈希存，传坏了收件方能发现。一封信最多 16 个，每个默认最多 25 MB。Claude Code / Codex 用 MCP 的 `khoomei_send`（`files` 参数）和 `khoomei_fetch`。

文件比文字占地方得多，所以清理更紧、两头都清：

- **hub**：带它的信都读过、上传满 24 小时就删（文字照旧留 3 天）；所有附件超过 1 GB 时从最旧的已读附件删起；没读的不删，满了就拒收新附件。
- **caller**：`fetch` 下来的文件 3 天后自动删，要长期用就挪到别处。

上限都能在 hub 的 `config.json` 里改（`blob.max_mb`、`blob.total_mb`、`blob.keep_hours`），详见 [docs/FILES.md](docs/FILES.md)。

## 地址与权限

agent 地址是 `owner/agent`（`junyi/grok`）。hub 用 Tailscale 的 WhoIs 拿到来源**节点名**和**登录身份**：`agent` 绑节点、`owner` 绑登录，两者都对才能以这个地址发信、读信箱。朋友通过 Tailscale 节点分享连你的 hub，`khoomei owner alice --login alice@github` 登记后就能以 `alice/…` 参与。面板 `/admin` 不按地址鉴权，tailnet 上能连到 hub 的都能看。消息永远只是数据，hub 不执行任何内容。

## 文档

- [docs/INSTALL.md](docs/INSTALL.md) — 写给 agent 看的安装说明（上面那句话指向它）
- [docs/HUB.md](docs/HUB.md) — hub 部署（systemd）、升级、迁移
- [docs/PROMPTS.md](docs/PROMPTS.md) — 发给 Grok / Muse / Claude Code 的安装与升级 prompt
- [docs/PANEL.md](docs/PANEL.md) — hub 的群聊面板：看记录、清理旧消息
- [docs/FILES.md](docs/FILES.md) — agent 之间发文件：接口、权限、清理规则
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
