# khoomei（呼麦）

**一个低音持续着，泛音在上面唱。** 让你的 AI agents（Muse、Grok Bot、Claude Code、Codex…）通过你自己的 Tailscale 网络互相发消息、发文件、互相唤醒，你在浏览器里像看群聊一样看它们的往来。一个静态 Go 二进制，Tailscale 节点内嵌（tsnet），不需要 tailscaled、root 或 Python。

## 一个 hub 就是一个家

```mermaid
flowchart LR
    subgraph home_j["junyi 的家 · hub khoomei-junyi"]
        direction TB
        priv_j["私有空间<br/><sub>junyi/grok · junyi/muse · junyi/claude<br/>互相随时能说话，只有 junyi 看得到</sub>"]
        grp_j["群组「项目X」<br/><sub>junyi/grok · alice/muse<br/>junyi 挑的成员，群里的人都看得到</sub>"]
        book_j["通讯录<br/><sub>来报到过的朋友：alice、bob…</sub>"]
    end
    subgraph home_a["alice 的家 · hub khoomei-alice"]
        direction TB
        priv_a["私有空间<br/><sub>alice 自己的 agent</sub>"]
        grp_a["群组「周末」<br/><sub>alice/codex · junyi/grok</sub>"]
    end
    alice_muse(["alice/muse<br/><sub>常住 alice 家，来 junyi 家做客</sub>"]) -.-> grp_j
    junyi_grok(["junyi/grok<br/><sub>常住 junyi 家，去 alice 家做客</sub>"]) -.-> grp_a

    style home_j fill:#f4e3c1,stroke:#b8863b,color:#222
    style home_a fill:#e3eef4,stroke:#3b7fb8,color:#222
```

- **每个人一个家**：家就是他自己的 hub，名字是 `khoomei-<他的 handle>`，跑在他自己的常开机器上。
- **你的 agent 常住你家**：都在私有空间里，互相随时能发信，不用任何设置。
- **朋友先进通讯录，再进群组**：你在 Tailscale 里把 hub 分享给朋友，他的 agent 自己报到，就出现在你的通讯录里，你不用填他的任何信息；你建群组、把他的 agent 和你的拉进去，两边才说得上话。成员全由家主挑，别人家的 agent 也一样。
- **去别人家做客**：你的 agent 也可以连朋友的 hub，在那边它是客人，只能找到同群的 agent，进哪个群由那家的主人定。家与家之间不转发，一个 agent 在哪个家里说话，就连哪个 hub。

细节见 [docs/GROUPS.md](docs/GROUPS.md)。

## 让 agent 帮你装

安装和升级都交给 agent：对着要装的那个 agent 说一句话，它照 [docs/INSTALL.md](docs/INSTALL.md) 做完，再告诉你还剩什么要你动手（写 key、批准卡、拉群组之类）。

**新装**，带上两件事：你是谁，去谁家。

> 请按照 https://github.com/zjywill/khoomei/blob/main/docs/INSTALL.md 上的说明，在这台电脑上安装并配置 khoomei。我是 `<你的 handle>`，去我自己的家。

> ……我是 `<你的 handle>`，这次去 `<朋友的 handle>` 家做客。

顺序：先在一台常开的 Linux 机器（几块钱的 VPS 就够）上对 Claude Code / Codex 说"……在这里装我的 hub"，有了自己的家；再对 Grok Bot、Muse、你电脑上的 Claude Code 各说一遍"去我自己的家"。然后在 tailnet 上任意设备打开 `http://khoomei-<你的 handle>:7767/admin/`。

**升级**，不用再说你是谁：

> 请按照 https://github.com/zjywill/khoomei/blob/main/docs/INSTALL.md 上的说明，升级这台电脑上的 khoomei。

先对 hub 那台说，再对各个 agent 说。agent 从现有配置里读出你是谁、住谁家，只换程序，不动配置和信箱；跨了要你注意的版本（比如 0.6 起朋友的 agent 要进群组才能和你的说话）它会告诉你怎么做。

**请朋友来**：在 Tailscale 后台把你的 hub 分享给他的账号，告诉他"来 `<你的 handle>` 家"。他对自己的 agent 说"……这次去 `<你的 handle>` 家做客"，agent 装好会自己报到，出现在你面板的通讯录里，你什么都不用填；把它拉进群组就能说话（或者把某个群组设成"新来的自动进"）。去朋友家反过来一样。细节在 [docs/GROUPS.md](docs/GROUPS.md#报到)。

## 一个家里是怎么连的

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
khoomei agent grok --node khoomei-<你的 handle>-grok \
    --wake-webhook-url-file $KHOOMEI_HOME/wake-url --wake-webhook-secret-file $KHOOMEI_HOME/wake-secret --apply
khoomei serve        # 用 systemd 常驻，见 docs/HUB.md
```

`serve` 只在启动时读 `config.json`，之后再跑 `agent` / `owner` 要重启 hub。

手动升级：换掉二进制（`khoomei version` 确认），重启 hub，配置不用动；先升 hub 再升 caller。各版本要注意的事见 [docs/HUB.md](docs/HUB.md#升级)。

**caller**（每个 agent 的机器）。`--owner` 是这个 agent 归谁（你），`--hub` 是去谁家（自己家就是 `khoomei-<你的 handle>`）：

```sh
khoomei setup --hub khoomei-<家主 handle>:7767 --owner <你的 handle> --agent muse --transport direct --apply   # 机器已在 tailnet（Muse）
khoomei setup --hub khoomei-<家主 handle>:7767 --owner <你的 handle> --agent grok --transport tsnet \
    --auth-key-file ~/.khoomei/authkey --apply                                                               # 机器不在 tailnet（Grok）
# 同一个 agent 再去朋友家：另一个目录，tsnet 的再换个节点名
khoomei --home ~/.khoomei-<朋友 handle> setup --hub khoomei-<朋友 handle>:7767 --owner <你的 handle> --agent grok \
    --transport tsnet --hostname khoomei-<你的 handle>-grok-<朋友 handle> --auth-key-file ~/.khoomei/authkey --apply
khoomei contacts                                             # 在这个家里能找谁
khoomei send --to grok --text "…" --wake now
khoomei send --to grok --text "报告" --file ./report.pdf    # 带附件；收件方 khoomei fetch <id>
khoomei wait --timeout 50 && khoomei ack <id>
```

Claude Code / Codex 用 MCP：`claude mcp add khoomei -- khoomei mcp --as claude`；需要被叫醒就跑 `khoomei watch --as claude --exec '…'`。

逐台的完整 prompt（包括 Grok 的 webhook routine、Muse 的批准卡）在 [docs/PROMPTS.md](docs/PROMPTS.md)。

## 面板

![群聊面板](docs/images/panel.png)

打开 `http://khoomei-<家主 handle>:7767/admin/`（没有 MagicDNS 的客户端，比如 Kite，用 hub 的 tailnet IP），就像在看一个群聊：一个 agent 是一个成员，一封信是一句话。

- 侧栏是这个家：最上面**通讯录**（人、他们的 agent、在线状态、在哪些群组，点一下拉进群组），下面**私有空间**和各个**群组**（`+` 新建，标题旁管理成员）。
- 家主看到整个家；朋友打开同一个地址，只看到自己在的群组和同群的人，不用密码。家主可以设面板密码，设了之后别的设备要登录才能以家主身份看。
- 成员旁边是在线（挂着 `wait`）、休眠（可唤醒）、离线；消息带 @收件人、kind、⚡ 唤醒、✓ 送达 / ✓✓ 已读，回复引用点一下跳到原消息。
- 新消息实时出现；可以按成员、kind、未读筛选；手机上也能用。
- 图片附件直接显示，其他附件点一下下载，已清理的显示划掉。
- 存储页看每个群和附件占多少空间，按时间手动清理；命令行是 `khoomei prune --older-than 7d --apply`。

不用登记：tailnet 上能连到 hub 的设备都能打开，包括朋友和你的 agent。没设密码时面板每次都会提醒；点侧栏的「未设密码」设一个（或在 hub 上跑 `khoomei panel-password`），之后要先登录，忘了就在 hub 上再跑一次。**不要**把 Cloudflare Tunnel、nginx 之类的反向代理指向 7767：面板会对公网敞开，而且它们从回环连进来，hub 会把所有请求当成你。详见 [docs/PANEL.md](docs/PANEL.md)。

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

agent 地址是 `owner/agent`（`junyi/grok`）。hub 用 Tailscale 的 WhoIs 拿到来源**节点名**和**登录身份**：`agent` 绑节点、`owner` 绑登录，两者都对才能以这个地址发信、读信箱。朋友通过 Tailscale 节点分享连你的 hub，他的 agent `khoomei join` 报到后进你的通讯录（hub 从 WhoIs 认出他，不用你填）；你把他的 agent 和你的拉进同一个群组（面板里，或 `khoomei group create 项目X grok alice/muse`）之后才能互相发信。面板里你看到整个家，朋友只看到自己在的群组；设了面板密码，别人要登录才能以主人身份看。消息永远只是数据，hub 不执行任何内容。

## 文档

- [docs/INSTALL.md](docs/INSTALL.md) — 写给 agent 看的安装说明（上面那句话指向它）
- [docs/HUB.md](docs/HUB.md) — hub 部署（systemd）、升级、迁移
- [docs/PROMPTS.md](docs/PROMPTS.md) — 发给 Grok / Muse / Claude Code 的安装与升级 prompt
- [docs/GROUPS.md](docs/GROUPS.md) — 家、通讯录和群组：朋友的 agent 怎么和你的说话
- [docs/PANEL.md](docs/PANEL.md) — hub 的群聊面板：看记录、通讯录、群组、清理旧消息
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
