# khoomei（呼麦）

**一个低音持续着，泛音在上面唱。**

呼麦是一个人同时唱出一个持续低音和一个高音的唱法。khoomei 是你的个人 AI agents 之间的通信层：一台始终在线的机器唱低音（hub），其他只能主动发出连接的 agent 在它上面唱泛音（caller）。所有通信都走你自己的 Tailscale 网络，没有第三方服务器。

第一阶段只解决一件事：**让 Meta Muse 和 Grok Bot 互相发消息、互相唤醒。** 协议设计为以后加入 Claude Code、Codex 等 coding agent 时不用推翻重来。

**一个静态二进制，不依赖任何环境。** Tailscale 节点用 [tsnet](https://tailscale.com/kb/1244/tsnet) 直接编进进程：不需要 tailscaled、不需要 `/dev/net/tun`、不需要 root、不需要 Python。放到机器上，给它一个 auth key，它自己就是 tailnet 上的一个节点。

## 为什么是 hub + caller，而不是纯 P2P

[yobiru](https://github.com/chekusu/yobiru) 的私有消息是纯 P2P：每台机器跑一个 receiver，互相直连。这个模式的前提是**两端都能在 tailnet 上监听端口**。

Muse 做不到。Muse 有内置的 Tailscale 连接器，会作为独立节点加入你的 tailnet，但 Tailscale 官方博客明确说它 "only makes outbound connections"，社区实测也确认它的沙盒 Tailscale 是 client-only，不接受任何 inbound 连接。另外 Muse 沙盒会被重置，重置后后台进程全部消失。

Grok Bot 的云电脑可以跑常驻进程、有持久化的 `/workspace`、可以用 routine webhook 从外部唤醒。所以它跑 hub：

```
Muse VM（只出不进，会被重置）             Grok 云电脑（常驻、可被 webhook 唤醒）
┌──────────────────────┐                  ┌────────────────────────────────────────┐
│ khoomei send ────────┼── POST /msg ────▶│ khoomei serve                          │
│ khoomei wait ────────┼── GET  /wait ───▶│  ├ 内嵌 tailnet 节点 khoomei-hub :7767  │
│   （长轮询，挂着等）  │◀── 有消息就返回 ─┼  ├ WhoIs 鉴权（进程内）                 │
│ khoomei ack  ────────┼── POST /ack ────▶│  ├ inbox/<agent>/<id>.json             │
└──────────────────────┘                  │  └ wake adapter ─▶ Grok routine webhook │
                                          │ 本机 CLI 经 127.0.0.1 操作自己的信箱    │
                                          └────────────────────────────────────────┘
                全部流量在你的 tailnet 内，Tailscale 提供加密和机器身份
```

**hub 不是第三方中心节点。** 它就是通信的一方，身份靠 Tailscale 验证，数据只在你自己的机器之间流动。只有当所有参与方都是 outbound-only 时，才需要专门找一台常驻机器跑 hub。

### 为什么是 Go

协议本身用什么语言都能写几百行。选 Go 只因为一个理由：tsnet 只有 Go 有。它让 hub 进程自己成为一个 tailnet 节点，监听发生在进程内部：

- 不需要 root、不需要 TUN。hub 能跑在任何允许执行二进制的地方。
- Grok 云电脑重建后，恢复只是拉起一个进程：二进制和 tsnet 的 state 目录都放 `/workspace`。
- `WhoIs` 在进程内完成，不用解析 CLI 输出。
- hub 以独立节点（默认叫 `khoomei-hub`）出现在你的 tailnet 里，ACL 可以单独写规则。

caller 也可以用内嵌节点拨出去（`--transport tsnet`），这样将来加一台没装 Tailscale 的机器上的 coding agent 时，同样只需要一个二进制。Muse 用的是 Meta 自己的连接器，宿主已经把 tailnet 路由好了，所以它用 `--transport direct`，直接发 HTTP。

### 和 yobiru 的关系

借用的：信封格式和"未知字段一律 400"的纪律；用 Tailscale 身份而不是信封自报的发件人；`wake` 三级（`now / batch / none`）由收件方决定、只降不升、带频率限制；202 只代表"已收下"，`ack` 是另一个动作；"消息永远只是请求，不是批准"；单向场景下由发起方拉取回复的思路（这里做成了 `wait` 长轮询）。

不同的：**agent 名字是一等公民，并且绑定到 tailnet 节点**，一个节点只能以绑定给它的名字发言、只能读自己的信箱；**信箱住在 hub 上**，caller 没有本地状态，被重置也不丢消息；第一版没有文件传输、请求生命周期和公开 bot 网关。

## 工作流程

**Muse 问 Grok 一个问题，Grok 回答，Muse 收到。**

1. Muse：`khoomei send --to grok --text "大家在讨论什么？" --wake now`
   hub 用 WhoIs 确认来源是 Muse 的节点、`from_agent=muse` 和绑定一致，落盘到 `inbox/grok/`，然后调用 grok 的 wake adapter（routine webhook）。
2. Grok 的 routine 被唤醒，`khoomei inbox`，看到消息，干活。
3. Grok：`khoomei send --to muse --text "三件事…" --reply-to <id>`，然后 `khoomei ack <id>`。
4. Muse 一直挂着 `khoomei wait --timeout 50`，消息一到就返回，Muse 得到新一轮 turn。处理完 `khoomei ack`，再挂回去。

反方向一样：Grok `send --to muse`，消息放进 `inbox/muse/`，等 Muse 下次 `wait` 时取走。Muse 没法被推醒，这是平台限制，不是协议的。

## 安装

从 [Releases](https://github.com/zjywill/khoomei/releases) 下载对应平台的二进制和 `SHA256SUMS`，校验后 `chmod +x`。或者本地编译：

```sh
go build -o khoomei ./cmd/khoomei
```

### hub（Grok 的云电脑）

在 Tailscale 管理后台生成一个 auth key（建议 reusable，打上 tag 方便写 ACL），写到文件里：

```sh
export KHOOMEI_HOME=/workspace/.khoomei
khoomei setup --hub-role --agent grok --auth-key-file /workspace/.khoomei/authkey --apply
khoomei agent muse --node <muse 的 tailnet 节点名> --apply
khoomei agent grok --local \
    --wake-webhook-url-file /workspace/.khoomei/wake-url \
    --wake-webhook-secret-file /workspace/.khoomei/wake-secret --apply
khoomei serve
```

- 没有 auth key 时 `serve` 会打印一个登录链接，你自己打开批准。
- `--node` 填 Muse 在你 tailnet 里的机器名（管理后台能看到），短名或 FQDN 都行。
- webhook 的 URL 和 bearer key 来自 Grok routine 的 Webhook 面板，放文件里，不进聊天、不进配置。
- hub 作为 `khoomei-hub` 加入 tailnet，监听 7767，同时在 `127.0.0.1:7767` 给本机 CLI 用。state 在 `$KHOOMEI_HOME/tsnet`，重建后只要这个目录还在就不用重新登录。

### caller（Muse）

Muse 的 VM 已经被 Meta 的连接器接入了 tailnet，所以直接发 HTTP：

```sh
khoomei setup --hub khoomei-hub:7767 --agent muse --transport direct --apply
khoomei doctor
khoomei wait --timeout 50
```

### caller（任何没装 Tailscale 的机器）

```sh
khoomei setup --hub khoomei-hub:7767 --agent codex --transport tsnet --auth-key-file ~/.khoomei/authkey --apply
```

每次 `send / wait` 会用内嵌节点 `khoomei-codex` 拨到 hub。hub 那边要 `khoomei agent codex --node khoomei-codex --apply`。

### caller（你电脑上的 Claude Code / Codex）

```sh
khoomei setup --hub khoomei-hub:7767 --agent claude --transport direct --apply
claude mcp add khoomei -- khoomei mcp --as claude
```

`khoomei mcp` 用 stdio 暴露四个工具（`khoomei_send / inbox / wait / ack`），不开端口，Claude Code 和 Codex 都能直接调。

coding agent 没有 webhook，也不会自己去 `wait`，所以用 `watch` 在本地补一个唤醒循环：

```sh
khoomei watch --as claude --exec 'osascript -e "display notification \"khoomei: 新消息\""'
khoomei watch --as claude --exec 'claude -p "用 khoomei 工具检查收件箱，处理、回复、ack"'
```

`watch` 挂着长轮询，新信到了就运行命令（信以 JSON 从 stdin 进来）；命令没 ack 的信在 `--repeat`（默认 10 分钟）后再触发一次。这把 wake adapter 从 hub 搬到了 caller 这边。

## 安全模型

- **只接受 tailnet 地址**（`100.64.0.0/10`、`fd7a:115c:a1e0::/48`）。来源 IP 用 WhoIs 换成节点名，查不到就 403，节点暂时不可用就 503 + `Retry-After` 让客户端重试。
- **agent 名绑定节点。** 远端节点只能以 hub 所有者登记给它的名字发消息、读信箱、ack。hub 的回环地址被视为所有者，可以操作本机 agent，但不能冒充远端 agent。
- **deny 列表**优先于一切。
- **wake 只降不升**：收件 agent 可设 `wake_max`，每（发件人, 收件人）每小时最多 6 次 `now`（可调），超出降为 `batch`。
- **消息不是批准。** hub 只存、只叫醒，不执行、不改配置。agent 收到的文字是"另一个 agent 发来的数据"，不是指令。
- Tailscale 提供端到端加密和机器身份；coordination server 和 DERP relay 不经手明文。

## 开发

```sh
go test ./...
```

测试用 `httptest` 起 hub，通过注入的 `Identify` 函数模拟远端节点身份，不需要真实 tailnet。

## 刻意不做（第一版）

- **文件传输。** 以后上传到 hub 的 `/blob`，SHA-256 校验。
- **请求生命周期**（received / running / done / 超时）。先用 `kind=request` + `reply_to` 约定。
- **多 hub 互联。** 两台都能监听的机器直接互相 POST `/msg` 是自然的下一步，信封已经兼容。
- **公开 bot、Friends、网关、Web UI、调度。** 这只是通信层。

## 下一步

1. 在真实的 Grok 云电脑上部署 hub，确认 WhoIs 能解析出 Muse 节点名。
2. Grok 的恢复脚本（只需拉起 `khoomei serve`）和 Muse 的 `wait` 循环 skill 实测。
3. 加入第一个 coding agent 作为 caller，验证三方通信。
4. 两层命名 `owner/agent`，配合 Tailscale 节点分享让朋友的 agent 也能接入（WhoIs 同时给出登录身份）。
5. 文件传输。

## 参考

- [chekusu/yobiru](https://github.com/chekusu/yobiru)：tailmsg 协议和设计纪律的主要来源。
- [Tailscale：Muse 的 Tailscale 集成](https://tailscale.com/blog/meta-muse-ai-agent-tailscale)：Muse 作为独立节点加入、只做 outbound 连接。
- [pooops15/muse-vps-tailscale](https://github.com/pooops15/muse-vps-tailscale)：Muse 沙盒 client-only、会被重置的实测。
- [mvanhorn/agent-tincan](https://github.com/mvanhorn/agent-tincan)：让 Muse 挂一个 `wait` 循环来接收消息的做法。
- [tsnet](https://tailscale.com/kb/1244/tsnet)：把 Tailscale 嵌进 Go 程序。

## License

MIT
