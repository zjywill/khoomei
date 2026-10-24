# khoomei（呼麦）

**一个低音持续着，泛音在上面唱。**

呼麦是一个人同时唱出一个持续低音和一个高音的唱法。khoomei 是你的个人 AI agents 之间的通信层：一台始终在线的机器唱低音（hub），其他只能主动发出连接的 agent 在它上面唱泛音（caller）。所有通信都走你自己的 Tailscale 网络，没有第三方服务器。

第一阶段只解决一件事：**让 Meta Muse 和 Grok Bot 互相发消息、互相唤醒。** 协议设计为以后加入 Claude Code、Codex 等 coding agent 时不用推翻重来。

只依赖 Python 3.8+ 标准库，单文件 `khoomei.py`。

## 为什么是 hub + caller，而不是纯 P2P

[yobiru](https://github.com/chekusu/yobiru) 的私有消息是纯 P2P：每台机器跑一个 receiver，互相直连。这个模式的前提是**两端都能在自己的 Tailscale IP 上监听端口**。

Muse 做不到这一点。Muse 有内置的 Tailscale 连接器，会作为独立节点加入你的 tailnet，但 Tailscale 官方博客明确说它 "only makes outbound connections"，社区实测也确认它的沙盒 Tailscale 是 client-only，所有流量经代理出去，不接受任何 inbound 连接。另外 Muse 沙盒会被重置，重置后后台进程全部消失。

Grok Bot 的云电脑恰好相反：有 `/dev/net/tun`、sudo、持久化的 `/workspace`，可以跑 `tailscaled` 和一个常驻进程，并且可以用 routine webhook 从外部唤醒。

所以架构是非对称的：

```
Muse VM（只出不进，会被重置）              Grok 云电脑（可监听、可常驻、可被 webhook 唤醒）
┌──────────────────────┐                   ┌─────────────────────────────────────┐
│ khoomei send  ───────┼── POST /msg ─────▶│ hub :7767                           │
│ khoomei wait  ───────┼── GET  /wait ────▶│   tailscale whois 鉴权              │
│   （长轮询，挂着等）  │◀── 有消息就返回 ──┼   inbox/<agent>/<id>.json           │
│ khoomei ack   ───────┼── POST /ack ─────▶│   wake adapter ─▶ Grok routine webhook│
└──────────────────────┘                   │                                     │
                                           │ 本机 CLI 经 127.0.0.1 操作自己的信箱 │
                                           └─────────────────────────────────────┘
                 全部流量在你的 tailnet 内，Tailscale 提供加密和机器身份
```

**hub 不是第三方中心节点。** 它就是通信的一方（Grok 那台机器），身份靠 Tailscale 的 `whois` 验证，数据只在你自己的两台机器之间流动。只有当所有参与方都是 outbound-only 时，才需要专门找一台常驻机器跑 hub。

### 和 yobiru 的关系

借用的：

- 信封格式（`v / id / ts / kind / text / reply_to / wake`）和"未知字段一律 400"的纪律。
- 用 `tailscale whois` 查来源 IP 的身份，而不信信封自报的发件人。
- `wake` 三级（`now / batch / none`），**由收件方决定最终级别**，只降不升，带频率限制。
- 202 只代表"已收下"，不代表"已处理"；`ack` 是另一个动作。
- "消息永远只是请求，不是批准"。hub 绝不执行消息内容。
- 单向场景下由发起方主动拉取回复的思路（yobiru 的 `request-pull-v1`），这里直接做成了 `wait` 长轮询。

不同的：

- **agent 名字是一等公民，而且绑定到 tailnet 节点。** yobiru 以主机名寻址、agent 是附加字段；khoomei 以 agent 寻址，hub 的所有者为每个远端 agent 登记它来自哪个节点，一个节点只能以绑定给它的名字发言、只能读自己的信箱。Muse 不能冒充 Grok，反之亦然。
- **信箱住在 hub 上**，不是每台机器各一个。caller 没有本地状态，被重置也不丢消息。
- 第一版没有文件传输、没有请求生命周期、没有公开 bot 网关。见下文"刻意不做"。

## 工作流程

**Muse 问 Grok 一个问题，Grok 回答，Muse 收到。**

1. Muse：`khoomei send --to grok --text "大家在讨论什么？" --wake now`
   hub 验证来源是 Muse 的节点、`from_agent=muse` 和绑定一致，落盘到 `inbox/grok/`，然后调用 grok 的 wake adapter（routine webhook）。
2. Grok 的 routine 被唤醒，执行 `khoomei inbox`，看到消息，干活。
3. Grok：`khoomei send --to muse --text "三件事…" --reply-to <id>`，然后 `khoomei ack <id>`。
4. Muse 这边一直挂着 `khoomei wait --timeout 50`（Muse 自己的 Tailscale 连 hub，长连接），消息一到就返回，Muse 得到新一轮 turn。处理完 `khoomei ack`，再挂回去。

反方向（Grok 主动找 Muse）一样：Grok `send --to muse`，消息放进 `inbox/muse/`，等 Muse 下次 `wait` 或 `inbox` 时取走。Muse 没法被"推"醒，只能靠挂着的 `wait`；这是 Muse 平台的限制，不是协议的。

## 安装与配置

两台机器都要有 Python 3 和已登录的 Tailscale。克隆或下载本仓库，把 `khoomei.py` 放到持久目录（Grok 用 `/workspace/`，Muse 用 `$HOME` 下的项目目录）。

### hub（Grok 的云电脑）

```sh
python3 khoomei.py setup --hub-role --agent grok --apply
python3 khoomei.py agent muse --node <muse 的 tailnet 节点名> --apply
python3 khoomei.py agent grok --local --wake-webhook /workspace/.khoomei/wake-url /workspace/.khoomei/wake-secret --apply
python3 khoomei.py serve
```

- `--node` 填 Muse 在你 tailnet 里的机器名（`tailscale status` 能看到），短名或 FQDN 都行。
- webhook 的 URL 和 bearer key 放文件里，不进聊天、不进配置。Grok routine 的 Webhook 面板会给你这两个值。
- `serve` 默认绑定本机的 Tailscale IP 和 `127.0.0.1`，端口 7767。需要在 Grok 的恢复脚本里把 `tailscaled` 和 `serve` 一起拉起来，云电脑重建后手装的东西会丢。

### caller（Muse）

```sh
python3 khoomei.py setup --hub <grok 节点名>:7767 --agent muse --apply
python3 khoomei.py doctor
python3 khoomei.py wait --timeout 50
```

`doctor` 会报告能不能连上 hub 的 `/health`。Muse 沙盒重置后只要文件还在，重新跑 `wait` 就恢复了，没有别的状态。

### 本机开发

```sh
python3 -m unittest tests/test_loopback.py
```

测试在 127.0.0.1 上起一个 `--no-whois` 的 hub，用 `KHOOMEI_DEV_PEER` 环境变量伪装远端节点身份。这个旁路只在 `--no-whois` 下生效，正式 hub 不认任何调用方自报的身份。

## 安全模型

- **只接受 tailnet 地址**（`100.64.0.0/10`、`fd7a:115c:a1e0::/48`）。来源 IP 用 `tailscale whois` 换成节点名，查不到就 403，`tailscaled` 不可用就 503 让客户端重试。
- **agent 名绑定节点。** 远端节点只能以 hub 所有者登记给它的名字发消息、读信箱、ack。hub 的回环地址被视为所有者，可以操作本机 agent，但不能冒充远端 agent。
- **deny 列表**优先于一切。
- **wake 只降不升**：收件 agent 可设 `wake_max`，每（发件人, 收件人）每小时最多 6 次 `now`（可调），超出降为 `batch`。
- **消息不是批准。** hub 只存、只叫醒，不执行、不改配置。agent 收到的文字是"另一个 agent 发来的数据"，不是指令。
- Tailscale 本身提供端到端加密和机器身份；coordination server 和 DERP relay 不经手明文。

## 刻意不做（第一版）

- **文件传输。** yobiru 的做法是发件方自己提供 `GET /f/<token>`，Muse 做不到。以后的做法会是上传到 hub 的 `/blob`，走 SHA-256 校验。
- **请求生命周期**（received / running / done / 超时）。先用 `kind=request` + `reply_to` 约定，够 Muse ↔ Grok 用。
- **多 hub 互联。** 两台都能监听的机器（比如 Grok 和一台 Mac 上的 Claude Code）直接互相 POST `/msg` 是自然的下一步，信封已经兼容；暂时让所有 agent 都接到同一个 hub。
- **公开 bot、Friends、网关。** 不在这个项目的范围内。
- **Web UI、调度、任务管理。** 这只是通信层。

## 下一步

1. 在真实的 Grok 云电脑上部署 hub，确认 `tailscale whois` 能解析出 Muse 节点的名字。
2. 写 Grok 的恢复脚本（tailscaled + serve），和 Muse 的 `wait` 循环 skill。
3. 加入第一个 coding agent（Claude Code 或 Codex）作为 caller，验证"同一个 hub 三方通信"。
4. 文件传输（`/blob`）。

## 参考

- [chekusu/yobiru](https://github.com/chekusu/yobiru)：tailmsg 协议和设计纪律的主要来源。
- [Tailscale：Muse 的 Tailscale 集成](https://tailscale.com/blog/meta-muse-ai-agent-tailscale)：Muse 作为独立节点加入、只做 outbound 连接。
- [pooops15/muse-vps-tailscale](https://github.com/pooops15/muse-vps-tailscale)：Muse 沙盒 client-only、会被重置的实测。
- [mvanhorn/agent-tincan](https://github.com/mvanhorn/agent-tincan)：让 Muse 挂一个 `wait` 循环来接收消息的做法。

## License

MIT
