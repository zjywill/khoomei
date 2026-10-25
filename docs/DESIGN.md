# 设计

## 为什么是 hub + caller，不是纯 P2P

[yobiru](https://github.com/chekusu/yobiru) 的私有消息是纯 P2P：每台机器跑 receiver 互相直连，前提是两端都能在 tailnet 上监听。Meta Muse 做不到：它有内置 Tailscale 连接器，但只做 outbound，沙盒不接受任何 inbound 连接，而且会被重置。所以需要一个始终可连的端，其他人都连它。

hub 不是第三方中心节点：它就是你的一台机器，身份靠 Tailscale 验证，数据只在你自己的机器之间流动。

## 为什么是 Go

只因为 tsnet 只有 Go 有。它让 hub 进程自己成为 tailnet 节点，监听在进程内：不要 root、不要 TUN、不要 tailscaled，机器重建后拉起进程就恢复；WhoIs 在进程内完成。caller 也能用同一个二进制自带节点拨出去（`--transport tsnet`），所以没装 Tailscale 的机器一样接入。

## 两个方向的机制不同

- **推**：发信带 `--wake now`，hub 落盘后调收件 agent 的 wake adapter（webhook 或 exec）。Grok Bot 的 routine webhook 实测 13 秒内醒来、处理、ack。
- **拉**：hub 没法连 Muse，Muse 自己挂 `wait`（长轮询，有信立即返回）。Muse 没有任何外部触发入口（只有按时间的 scheduled tasks），这是平台上限。

`wake` 由收件方决定、只降不升、每（发件人，收件人）每小时限次。202 只表示"已收下"；`ack` 是收件方自己的动作，没 ack 的信下次还在。

## 地址：`owner/agent`

WhoIs 给出来源的**节点名**和**登录身份**，地址两层各绑一个。远端只能以"节点匹配且登录匹配"的地址发信、读信箱、ack；hub 回环是所有者，可操作本机 agent，不能冒充远端 agent，也不能读朋友的信箱。朋友通过 Tailscale 节点分享连 hub，不用进你的 tailnet，也不要公网服务器。没设 owner 时裸名照旧（单所有者模式）。

## 各平台的坑（2026-10 实测）

**Muse**
- 只能用 hub 的名字，不能用 IP：沙盒把 tailnet 名解析成 `198.18.x.x` 假地址再透明转发，直接拨 `100.x` 返回 `EPERM`。
- 第一次连某个节点要在连接器弹的卡上批准（选长期）；批准前请求挂着，`doctor` 会超时。hub 换了机器算新设备，要再批准一次。
- 走沙盒的 `HTTP(S)_PROXY`；khoomei 的 direct 模式读这些变量。
- 沙盒重置后进程没了、`$HOME` 下文件还在；caller 无状态，重跑 `wait` 即恢复。

**Grok Bot**
- 云电脑会在空闲时停机，所以不能当 hub；当 caller 正合适：hub 调它的 routine webhook 就能把它叫起来。
- 机器上没有 Tailscale，用 `--transport tsnet`，节点 `khoomei-<owner>-grok`。每条 khoomei 命令会在进程内起一下节点，慢几秒。
- webhook 的 URL/key 只在 routine 的面板里可见，Grok 自己读不到，要人复制。
- routine 必须忽略自己发的信（`from_agent` 是自己时只 ack），否则自测会形成自回复。
- 两次 routine 运行可能重叠，处理前 `mkdir claims/<id>` 认领。

**Mac + Kite**：第三方 Tailscale 客户端，没有 `tailscale` CLI、没有 MagicDNS，连 hub 用 IP，或让 khoomei 用 tsnet 自带节点。

## 和 yobiru 的关系

借用：信封格式和"未知字段一律 400"；用 Tailscale 身份而不信信封自报；`wake` 三级只降不升带限流；202 ≠ 已处理；"消息永远不是批准"；单向场景由发起方拉取（这里是 `wait`，yobiru 0.7.9 的 connector 也改成了同样的长轮询）。
不同：agent 名是一等公民并绑定节点和登录；信箱住在 hub 上，caller 无状态；没有文件传输、请求生命周期、公开网关。

## 刻意不做

文件传输（将来 `/blob` 上传到 hub）、请求状态机、hub 之间转发、OAuth、公网暴露、对系统 tailscaled 的依赖、Web UI、调度。这只是通信层。

## 参考

[chekusu/yobiru](https://github.com/chekusu/yobiru) · [Tailscale：Muse 集成](https://tailscale.com/blog/meta-muse-ai-agent-tailscale) · [muse-vps-tailscale](https://github.com/pooops15/muse-vps-tailscale) · [agent-tincan](https://github.com/mvanhorn/agent-tincan) · [tsnet](https://tailscale.com/kb/1244/tsnet) · [Grok Bot routines](https://cursor.com/help/grok-bot/routines)
