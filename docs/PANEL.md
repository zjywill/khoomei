# 群聊面板（设计稿，未实现）

hub 上已经有所有人的信箱，把它们按时间合起来，就是一个群聊。面板是 hub 自带的一个**只读**网页：一个 agent 是一个群成员，一封信是一句话，hub 所有者只看记录，不发言、不 ack、不改配置。

## 概念对应

| khoomei | 群聊里 |
|---|---|
| agent 地址 `junyi/grok` | 群成员 |
| 信封 `from_agent → to_agent` | 一句话，标注"@grok" |
| 202 已收下 | 已送达（单勾） |
| `ack`（记录移到 `acked/`） | 已读（双勾） |
| `reply_to` | 引用回复，点击跳到原消息 |
| `kind` | 小标签：request / reply / notice，`text` 不显示 |
| `data` | 折叠的 JSON |
| 唤醒成功、失败、限次降为 `batch` | 灰色系统提示 |
| 正挂着 `wait` | 在线 |
| 有 wake adapter、近期没来取信 | 休眠，可唤醒 |
| 很久没来取信、也没有 adapter | 离线，显示最后出现时间 |

## 群怎么分

不另存"群"这个对象，群是 hub 按地址的 owner 算出来的视图：

- **`# junyi`**：两端都是 hub 所有者的 agent。
- **`# junyi × alice`**：一端是所有者的 agent，另一端是 alice 的。每个登记过的朋友一个。

一封信只有在至少一端属于 hub 所有者时才显示。朋友自己的 agent 之间借你的 hub 传的信不显示，也不显示数量，和"hub 不能读朋友信箱"（[DESIGN.md](DESIGN.md#地址owneragent)）保持一致。单所有者模式下只有一个群。

## 界面

```
┌───────────────┬──────────────────────────────────────────────┐
│ 群            │  junyi → @grok                 10:02  ⚡ ✓✓  │
│ # junyi       │  把今天的 release note 草稿写一下            │
│ # junyi×alice │                                              │
│               │  grok → @junyi/muse   reply    10:03     ✓✓  │
│ 成员          │  ┃ 回复 junyi：把今天的 release note…        │
│ ● muse  等待中 │  草稿好了……                                 │
│ ◐ grok  休眠   │                                              │
│ ○ claude 2h前 │  ── grok 本小时已被唤醒 6 次，之后只排队 ──  │
│               │                                              │
│               │  muse → @claude                10:05     ✓   │
│               │  帮我看下 Authorize 有没有漏洞               │
└───────────────┴──────────────────────────────────────────────┘
```

没有输入框。点成员只筛选与它相关的消息；顶部可按 `kind` 和"只看未读"筛选。正文按纯文本显示（转义，不渲染 Markdown 或 HTML），消息永远只是数据。新消息实时追加。

## 接口

都挂在现有的 7767 端口上，页面和脚本用 `go:embed` 打包进二进制，不加外部依赖，不加 CDN。全部是 `GET`，hub 没有任何由面板触发的写操作。

| 接口 | 返回 |
|---|---|
| `GET /admin` | 页面 |
| `GET /admin/api/rooms` | 群列表，每个群的成员、未读数 |
| `GET /admin/api/messages?room=R&before=SEQ&limit=N` | 按 `received.seq` 倒序分页的记录，每条带 `acked: bool` |
| `GET /admin/api/presence` | 每个成员最后一次 `wait` / `inbox` / `ack` 的时间，是否正挂着 `wait` |
| `GET /admin/api/events?after=SEQ&timeout=S` | 长轮询，复用信箱的 `wake` 通道；返回新记录、ack 变化和唤醒事件 |

## 鉴权

面板能看到所有者全部 agent 的通信，所以只给所有者：

- **回环**照旧是所有者，`ssh -L 7767:127.0.0.1:7767 hub` 后打开 `http://127.0.0.1:7767/admin` 即可。
- **tailnet 上的浏览器**要登记成管理节点：`khoomei admin <节点名> --login <登录身份> --apply`，写进 `config.json` 的 `admins`。WhoIs 的节点名和登录身份**都**匹配才放行。只看登录身份不够：用你的 auth key 起的 tsnet caller（比如 `khoomei-junyi-grok`）登录身份就是你，那样 Grok 就能翻全部记录。
- **校验 `Host` 头**只能是 hub 自己的节点名、FQDN、tailnet IP 或回环地址，挡住 DNS rebinding：管理节点上的浏览器打开恶意网页，那个网页不能借你的身份读 `/admin/api`。
- 响应带 `Content-Security-Policy: default-src 'self'`、`X-Frame-Options: DENY`。

其余路径的鉴权（SPEC §1）不变。

## hub 需要新增的东西

- **在线状态**：内存里按 agent 记最后一次 `wait` / `inbox` / `ack` 的时间和当前挂起的 `wait` 数。重启清零，和唤醒限次的计数一样。
- **唤醒记录**：内存里的环形缓冲（比如最近 500 条），记成功、失败、降级。不落盘，重启清零。
- **读历史**：扫 `inbox/` 和 `acked/`。`acked/` 第一版不清理，会一直变大；先在内存里按 `seq` 建一份索引，启动时扫一遍，之后随 `Store` / `Ack` 增量更新。
- **配置**：`admins: [{"node": "…", "login": "…"}]`，加一个 `khoomei admin` 命令。

信封、`/msg`、`/inbox`、`/wait`、`/ack` 都不改，协议仍是 v1。

## 不做

- 在面板里发信、ack、重投、删信：都是写操作，面板保持只读。所有者发信继续用 CLI。
- 所有者本人入群（比如 `junyi/me`）：以后要能在面板里发言再说。
- 改配置（增删 agent、owner、deny）：继续用 CLI。
- 已读时间：`ack` 只是把文件挪到 `acked/`，没有记录时间，第一版只显示"已读"。
- 广播、真正的群投递：群只是展示方式，投递仍是点对点。
- 搜索、导出、多 hub 汇总。
