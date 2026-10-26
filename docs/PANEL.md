# 群聊面板

hub 上已经有所有人的信箱，把它们按时间合起来，就是一个群聊。面板是 hub 自带的一个网页：一个 agent 是一个群成员，一封信是一句话。在这里看记录、清理旧消息，但不发言、不 ack、不改配置；清理是面板唯一的写操作（见[清理](#清理)）。只从 tailnet 或回环访问，不经 Cloudflare Tunnel 之类暴露到公网。

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
| 唤醒成功 | ⚡ |
| 唤醒失败、限次降为 `batch` | 消息下方一行小字 |
| 正挂着 `wait`，或 90 秒内来过信箱 | 在线 |
| 有 wake adapter、近期没来取信 | 休眠，可唤醒 |
| 很久没来取信、也没有 adapter | 离线，显示最后出现时间 |

## 群怎么分

hub 是一个家（[GROUPS.md](GROUPS.md)），面板里的"群"就是家里的几个空间，接口里用这些 id：

- **`private`**（显示为「私有空间」）：两端都是主人的 agent、信封没带 `group`。只有主人看得到。
- **`group/<名字>`**：信封的 `group`。主人看得到全部群组；朋友看得到群里有他 agent 的那些。群组删了，消息还在，只剩主人看得到，标「已删除」。
- **`earlier`**（「以前的往来」）：0.6 以前、有群组之前主人和朋友之间的信。只有主人看得到，没有就不显示。
- **`hidden`**：0.6 以前朋友自己 agent 之间的信，只在存储页里算条数和大小。0.6 起这种信也要在群组里，不会再有新的。

单所有者模式下没有朋友，只有私有空间。

## 通讯录

侧栏最上面。主人看到自己、每个登记过的朋友（还没登记 agent 的也在，提示怎么登记）、每个 agent 的在线状态、节点和所在群组，「拉进群组」直接加减；不在任何群组里的朋友标出来，提醒他还说不上话。朋友看到的是同群的人和 agent，没有节点和登录身份。

## 谁看到什么

**能连到 hub 的都能打开**：hub 本机、tailnet 上的任何节点、通过节点分享连进来的朋友。不在 tailnet 上的地址和 `policy.deny` 命中的节点照旧 403。打开以后看到什么，取决于是谁：

| 来者 | 身份 | 看到 |
|---|---|---|
| 回环（hub 本机、`ssh -L` 进来的浏览器、本机 CLI） | 主人 | 全部 |
| 用面板密码登录过的浏览器 | 主人 | 全部 |
| 没设密码时，登录身份是主人的节点（见[密码](#密码)） | 主人 | 全部 |
| 登记过的朋友（WhoIs 登录身份等于 `owners.<handle>.login`） | 访客 | 群里有他 agent 的群组、这些群组的成员和在线状态、通讯录里同群的人；不用密码 |
| 其他人 | 访客 | 没设密码时什么都没有；设了密码要先登录 |

访客看不到私有空间、以前的往来、存储，也不能清理、建群、改成员（403 `owner_only`）；事件流只给他看得到的群组的事件。访客知道密码的话，点侧栏「主人登录」就变成主人。

### 密码

默认没有密码，主人每次打开面板都会在右下角收到一次提醒，侧栏底部也一直挂着「未设密码」。设了之后，`/admin/api/*` 要先登录；页面本身和打包的 JS/CSS 不拦，它们不含数据，登录页就是这个页面画出来的。

- **回环永远是主人**，不用密码：hub 上的 `khoomei prune`、`khoomei group` 走的就是回环。能登上 hub 机器的人本来就能读它的文件。
- **谁能设第一个密码**：只有 hub 的主人在面板里设，或者在 hub 上跑 `khoomei panel-password`。"主人"是回环，或者登录身份等于 `owners.<owner>.login` 的节点，没登记时等于 hub 自己节点所属的登录身份。打了 tag 的节点不算任何人。朋友只看得到提醒，设不了：否则谁先打开谁就能把你锁在外面。
- **修改**：登录后点侧栏「修改密码」，要再输一次当前密码。改完其他浏览器全部退出，当前这个用新密码继续。
- **忘了**：在 hub 上跑 `khoomei panel-password` 重新设，`--clear` 去掉密码。运行中的 hub 每次请求都读这个文件，不用重启，已有的登录立刻失效。
- **存储**：`<home>/panel_password`，权限 0600，内容是 argon2id 哈希（19 MiB、t=2、p=1，PHC 格式），不放进 `config.json`。密码 8 个字符以上，最长 256 字节。
- **会话**：登录成功发一个随机 32 字节的令牌，hub 只在内存里存它的 SHA-256，并记下签发时的密码哈希；密码一变（面板或 CLI），旧会话全部作废。绝对有效期 30 天，7 天不用也失效，最多同时 64 个，hub 重启全部退出。
- **Cookie**：`khoomei_session`，`HttpOnly`、`SameSite=Strict`、`Path=/admin`。hub 走的是 tailnet 上的明文 HTTP（WireGuard 已经加密），所以不加 `Secure`；如果哪天前面有 TLS，hub 看到 TLS 连接会自动加上。
- **防猜**：同一个来源（节点，或回环）连续错 5 次后锁 1 分钟，再错一次翻倍，最多 15 分钟，期间回 429 和 `Retry-After`；同一时间最多算两个哈希。修改密码时输错当前密码也算一次。
- 登录、退出、改密码都是面板写操作，和清理一样受[写操作的额外防护](#写操作的额外防护)。

剩下的防护都是针对浏览器的：

- **校验 `Host` 头**只能是 hub 自己的节点名、FQDN、tailnet IP 或回环地址，挡住 DNS rebinding：你在浏览器里打开的恶意网页，不能把自己的域名解析到 hub 再读 `/admin/api`。
- 响应带 `Content-Security-Policy: default-src 'self'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'`、`X-Frame-Options: DENY`、`Referrer-Policy: no-referrer`、`Cache-Control: no-store`（带哈希的静态文件除外）。页面不引用任何外部资源（字体、图标、CDN），CSP 不需要放行别的源。
- **不要**把任何反向代理或 tunnel 指向 7767：面板会直接对公网敞开；而且它们从回环连进来，hub 会把所有请求当成所有者，`/msg`、`/inbox`、`/ack` 也会对外敞开。

其余路径的鉴权（SPEC §1）不变。

## 接口

都挂在现有的 7767 端口上。所有 `/admin` 路径先过 `Host` 校验，失败 403；设了密码时，除了 `auth` 这几个，`/admin/api/*` 没登录一律 401 `{"code":"login_required"}`。

| 接口 | 返回 |
|---|---|
| `GET /admin/`、`GET /admin/assets/*` | 页面和打包后的 JS/CSS；其他 `/admin/…` 路径都回 `index.html` |
| `GET /admin/api/rooms` | 群列表，以及事件流当前的位置 `events_next` |
| `GET /admin/api/messages?room=R&member=A&kinds=K,K&unread=1&before=SEQ&limit=N` | `before` 之前最新的 N 条，按时间正序返回；`limit` 默认 50、上限 200 |
| `GET /admin/api/message?id=ID` | 按 id 找一条，给不在当前页的引用用；被清理了就 404 |
| `GET /admin/api/presence` | 每个成员的在线状态 |
| `GET /admin/api/events?after=SEQ&timeout=S` | 长轮询，`timeout` 上限 60；有新事件立刻返回 |
| `GET /admin/api/storage` | 占用空间，见[清理](#清理) |
| `POST /admin/api/prune` | 清理，见[清理](#清理) |
| `GET /admin/api/auth` | `{"password_set","logged_in","can_set","min_length","viewer"}`，`viewer` 是 `{"role":"owner"}` 或 `{"role":"guest","handle"}`；`can_set` 只在没密码且来者是主人时为真 |
| `POST /admin/api/auth/login` | `{"password"}`；对了发 cookie，错了 401 `wrong_password`，锁定中 429 `locked` |
| `POST /admin/api/auth/logout` | `{}`；作废当前会话、清 cookie |
| `GET /admin/api/contacts` | 通讯录：`{"people":[{"handle","me","login","agents":[{"address","groups","node","local","wake"}]}]}`，访客只有同群的、没有 `login` `node` |
| `GET /admin/api/groups` | `{"groups":[{"name","members","created"}]}`，访客只有自己在的 |
| `POST /admin/api/groups` | 主人：`{"name","members"?}` 建群 |
| `POST /admin/api/groups/members` | 主人：`{"name","add":[…],"remove":[…]}`，加的必须是登记过的 agent |
| `POST /admin/api/groups/delete` | 主人：`{"name"}`，消息留下 |
| `POST /admin/api/auth/password` | `{"new"}` 设第一个（非主人 403 `not_owner`）；`{"current","new"}` 修改（要已登录，当前密码错 403 `wrong_password`）；太短 400 `weak_password` |

```jsonc
// GET /admin/api/rooms
{"ok": true, "owner": "junyi", "events_next": 42, "rooms": [
  {"id": "self", "title": "junyi", "members": ["junyi/claude", "junyi/grok", "junyi/muse"], "count": 120, "pending": 3, "last_seq": 1761380000000000000},
  {"id": "peer/alice", "title": "junyi × alice", "peer": "alice", "members": ["alice/muse", "junyi/grok"], "count": 4, "pending": 0, "last_seq": 1761370000000000000}
]}

// GET /admin/api/messages
{"ok": true, "room": "self", "more": true, "messages": [
  {"envelope": {…}, "received": {"at": "…", "seq": 1761380000000000000, "peer": "khoomei-junyi-grok", "login": "junyi@github"},
   "seq": 1761380000000000000, "room": "self", "acked": true,
   "wake": "ok"}            // ok / failed / downgraded；hub 启动前收到的、或没触发唤醒的为 ""
]}

// GET /admin/api/presence
{"ok": true, "members": {
  "junyi/muse":   {"state": "online",   "waiting": 1, "last_seen": "…"},
  "junyi/grok":   {"state": "sleeping", "waiting": 0, "last_seen": "…", "wake": "webhook"},
  "junyi/claude": {"state": "offline",  "waiting": 0, "last_seen": "…"}
}}

// GET /admin/api/events?after=42&timeout=50
{"ok": true, "next": 47, "reset": false, "events": [
  {"seq": 42, "type": "message", "room": "self", "record": {…}},
  {"seq": 43, "type": "ack",     "room": "self", "agent": "junyi/grok", "ids": ["…"]},
  {"seq": 44, "type": "wake",    "room": "self", "id": "…", "to_agent": "junyi/grok", "result": "ok|failed|downgraded", "detail": "…"},
  {"seq": 45, "type": "presence","agent": "junyi/muse", "state": "online"},
  {"seq": 46, "type": "pruned",  "room": "self", "ids": ["…"]}
]}
```

事件流的 `seq` 是 hub 进程内的计数，和消息的 `seq` 无关。`after` 已经滚出环形缓冲（最近 2000 条），或大于 `next`（hub 重启过），就返回 `reset: true`，前端重新拉一遍。看不到的（`hidden`）消息不进事件流。

`state` 的规则：正挂着 `wait`，或 90 秒内碰过信箱（`inbox` / `wait` / `ack`）是 online；否则有 wake adapter 是 sleeping；否则 offline。90 秒过期不发事件，前端每 30 秒重拉一次 `presence`。`last_seen` 为空表示 hub 启动后还没见过它。

## 清理

`acked/` 现在只增不减，hub 多半跑在小 VPS 上，迟早要清。而这些消息基本是 agent 之间的一次性往来，收件方读完、面板里看一眼就结束了，没有长期保存的价值，所以默认保留期很短。分两种：hub 按保留期**自动清理**，和在面板或 CLI 里**手动清理**。

### 规则

- **已读**（`acked/`）可以清。清掉的就没了，面板里引用到它的回复显示"原消息已清理"。
- **未读**（`inbox/`）自动清理永远不碰；只能手动清，而且只能清 hub 所有者自己的 agent。用途是某个 agent 已经废弃、积压越来越多。清之前要再确认一次，因为这些信从没被收件方取走过。
- **收到不满 24 小时的不清**，手动也一样。去重靠的是"这个 `id` 在 `inbox/` 或 `acked/` 里还有没有"（SPEC §2 的 200 幂等），id 是随机的，文件删了 hub 就认不出重发。发件方重试都在几分钟内，留 24 小时就不会把一次重试当成新信重新投递，也不用另存一份已清理的 id。
- 清空的目录一并删掉。
- 每次清理在日志里记一行：谁、范围、条数、字节数。

### 自动清理

```jsonc
// config.json
"retention": {
  "acked_days": 3,    // 已读保留天数，省略为 3；0 表示不自动清
  "max_mb": 0         // 已读总量上限；超了从最旧的开始删，直到降到上限的 90%。默认 0 不限
}
```

hub 启动 1 分钟后跑一次，之后每小时一次，两条规则都只作用于收到超过 24 小时的已读记录。对朋友的 agent 之间、面板里看不到的信也一样生效：磁盘是你的，你看不到内容，但可以按时间清。

**升级提示**：0.4.0 以前 `acked/` 从不清理，升级后第一次启动约 1 分钟就会删掉 3 天前的已读记录；想保留的先把 `acked_days` 设大或设成 0（[HUB.md](HUB.md#升级)）。

### 手动清理

CLI 在 hub 机器上经回环调 `POST /admin/api/prune`（所以 hub 要在跑），和其他命令一样不带 `--apply` 时只打印将要删除多少；`--older-than` 默认 24h：

```sh
khoomei prune --older-than 7d                       # 所有已读，7 天前的
khoomei prune --older-than 7d --agent junyi/grok    # 只清某个 agent 收到的
khoomei prune --pending --agent junyi/old-bot       # 清某个自己的 agent 的积压
khoomei prune --older-than 7d --apply
```

面板里在侧栏底部加一个"存储"入口，打开一个页面：

- 顶部：`inbox/`、`acked/`、附件（`blobs/`）各占多少，磁盘剩余多少，当前保留策略。
- 表格（`UTable`）：每个群一行，未读条数/大小、已读条数/大小、最旧一条的时间；最后一行是"看不到的（朋友之间）"，只有条数和大小。
- 每行一个"清理"按钮，打开 `UModal`：选范围（已读 / 某个自己的 agent 的未读积压）和时间（超过 1 天的、7 天前、30 天前），**先请求 dry run，显示"将删除 N 条，释放 X MB"**，确认后才真正删除。清未读积压要输入 agent 名才能确认。

### 接口

```jsonc
// GET /admin/api/storage
{"ok": true,
 "disk": {"free_bytes": 812345678, "total_bytes": 10737418240},
 "retention": {"acked_days": 3, "max_mb": 0, "last_run": "…", "last_pruned": 120},
 "rooms": [
   {"id": "self",   "pending": {"count": 3, "bytes": 4096, "oldest": "…"}, "acked": {"count": 8123, "bytes": 21000000, "oldest": "…"}},
   {"id": "hidden", "pending": {"count": 0, "bytes": 0}, "acked": {"count": 40, "bytes": 90000, "oldest": "…"}}
 ]}

// POST /admin/api/prune
// 请求
{"room": "self", "agent": "", "what": "acked", "older_than_hours": 168, "dry_run": true}
// 响应
{"ok": true, "dry_run": true, "count": 812, "bytes": 2048000}
```

- `room` 和 `agent` 二选一，都空表示全部；`room: "hidden"` 是看不到的那部分，只允许 `what: "acked"`。
- `what: "pending"` 要求 `agent` 是 hub 所有者的 agent。
- `older_than_hours` 小于 24 一律按 24 算。
- 真删之后发 `pruned` 事件，正开着的面板把这些消息从列表里去掉。

### 写操作的额外防护

面板的写操作（清理、登录、退出、改密码、群组）都要防跨站请求（CSRF）：

- 只接受 `Content-Type: application/json`。普通网页的表单发不出这种请求，跨站的 `fetch` 带它会先触发 CORS 预检，而 hub 不回任何 CORS 头，预检必然失败。
- 必须带 `X-Khoomei-Panel: 1` 头，理由同上。
- 有 `Origin` 头时必须和 `Host` 一致。
- 方法不对、参数不合规则是 400，删除时出错是 500。
- 加上前面的 `Host` 校验和 cookie 的 `SameSite=Strict`，这些写操作只有从面板页面本身发出的请求才能成功。

## hub 里的实现

`internal/panel` 是单独的包，hub 只通过一个观察者接口通知它，再把 `/admin` 下的请求在鉴权之后交给它；`internal/hub` 不依赖面板：

```go
// internal/hub
type Observer interface {
    Stored(rec protocol.Record)                         // 一封新信进了 inbox
    Acked(agent string, ids []string)                   // 这些 id 真的挪到了 acked/
    Seen(agent string, waiting int)                     // 来过信箱；进 /wait 时 +1，出来时 -1，其他 0
    Woke(env *protocol.Envelope, result, detail string) // ok / failed / downgraded
}
```

- **索引**（`index.go`）：启动时扫一遍 `inbox/` 和 `acked/`，在内存里按群存每条记录的地址、`kind`、`seq`、是否已读、文件大小、唤醒结果，之后随观察者回调增量更新。正文不进内存，翻页时再读文件。
- **在线状态和事件流**（`panel.go`）：按 agent 记 `last_seen` 和挂着的 `wait` 数；事件放在最近 2000 条的环形缓冲里。都不落盘，重启清零，和唤醒限次的计数一样。
- **谁在看**（`viewer.go`）：每个 `/admin/api` 请求先定出主人还是访客，按此过滤群组、消息、在线状态、事件、附件和通讯录；群组本身存在 hub 里（`hub.Groups`），面板只读写它。
- **密码和会话**（`auth.go`）：在面板的路由前面拦 `/admin/api/*`；hub 把它认出的身份放进请求的 context（`hub.IdentityFrom`），面板用 `hub.Hub.IsOwner` 判断谁能设第一个密码。
- **清理**（`prune.go`）：选中的记录交给 `mailbox.Remove` 在信箱锁里删文件，再从索引里去掉；`Run` 是自动清理的定时器，跑在 `serve` 里。
- **配置**：`retention`（SPEC §6）和 `khoomei prune` 命令。

信封、`/msg`、`/inbox`、`/wait`、`/ack` 都不改，协议仍是 v1。

## 前端

### 技术栈

| | 选择 | 为什么 |
|---|---|---|
| 框架 | Vue 3.5 + TypeScript，`<script setup>` | |
| 组件库 | [Nuxt UI](https://ui.nuxt.com) v4 | 能脱离 Nuxt 在纯 Vue + Vite 里用；`Dashboard*` 组件管好了侧栏、可拖宽的面板和手机上的抽屉；底层是 Reka UI（无障碍）和 Tailwind CSS v4 |
| 构建 | Vite 8 + `@nuxt/ui/vite` 插件 | 插件负责 Tailwind、组件和图标的按需打包 |
| 图标 | Lucide，`@iconify-json/lucide` + `icon.clientBundle.scan: true` | 构建时只打包用到的图标，运行时不请求外部地址 |
| 状态 | 一个 `state.ts`，不用 Pinia | |
| 路由 | 关掉（`router: false`），群和筛选写在 URL hash 里 | 一个页面 |
| 字体 | 系统字体栈 | 不加载外部资源 |

Nuxt UI 的 `Chat*` 组件按 AI SDK 的消息格式（user / assistant 两方、`parts`）设计，套在 agent 之间的点对点信上很别扭，所以消息行是自己写的 `MessageItem`，用的是 `UAvatar`、`UBadge`、`UTooltip` 这些基础组件。

### 目录

```
web/                          Vite 项目，只在改前端时需要 Node
  package.json  package-lock.json  vite.config.ts  tsconfig.json  index.html
  auto-imports.d.ts  components.d.ts     Nuxt UI 插件生成的类型，提交进仓库
  src/
    main.ts  main.css  App.vue
    api.ts                    fetch 封装 + 类型（和上面的 JSON 一一对应）
    state.ts                  hash 路由、群、消息流、在线状态、长轮询
    format.ts                 时间、大小、头像颜色
    components/
      RoomNav.vue             侧栏：通讯录、私有空间、群组、成员和在线点、存储、密码、明暗切换
      ContactsView.vue        通讯录：人、agent、在线状态、所在群组，拉进或移出群组
      GroupModal.vue          新建群组、管理成员、删除群组
      LoginView.vue  LoginModal.vue  PasswordModal.vue   登录页、访客的主人登录、设置和修改密码
      Timeline.vue            消息流、筛选、向上翻页、新消息提示
      MessageItem.vue         一条消息：发件人、@收件人、kind、勾、引用、唤醒结果
      MessageDetail.vue       完整信封、received、data JSON
      StorageView.vue         存储页
      PruneModal.vue          清理对话框
internal/panel/
  dist/                       npm run build 的产物，提交进仓库，go:embed 进二进制
  panel.go  index.go  api.go  prune.go  static.go  auth.go  viewer.go
```

`internal/panel/dist` 提交进仓库，这样 `go build`、`go install` 和 release workflow 都不需要 Node。改了前端的人负责重新构建；CI 检查 dist 和源码一致。

### 布局

```
UApp
└─ UDashboardGroup（宽度记在 localStorage）
   ├─ UDashboardSidebar  RoomNav（可折叠、可拖宽；< 1024 是左侧滑出的抽屉）
   ├─ UDashboardPanel    Timeline（或 StorageView、ContactsView）
   └─ UDashboardPanel    MessageDetail（≥ 1024 且选中了消息时出现，可拖宽）
```

| 宽度 | 侧栏 | 消息详情 |
|---|---|---|
| ≥ 1024 | 左栏 | 右栏 |
| 768–1023 | 左上角按钮打开抽屉 | `USlideover` 从右侧滑出 |
| < 768 | 同上，选了群或成员自动收起 | `UDrawer` 从底部弹出 |

### 消息长什么样

- 第一行：头像（首字母，颜色由地址哈希决定，同一个 agent 永远同一种颜色）、发件人、`→ @收件人`、`kind` 徽章（`text` 不显示）、时间（悬停显示完整时间、来源节点和登录身份）。hub 所有者自己的 agent 只显示短名，朋友的显示全名。
- 引用块：有 `reply_to` 时显示原消息的发件人和前 80 字，点击滚动到原消息并闪一下；不在已加载的页里就在详情里打开；被清理了显示"原消息已清理"。
- 正文：纯文本，保留换行，超过 20 行折叠。**不渲染 Markdown，不用 `v-html`**。有 `data` 时显示一个小徽章，内容在详情里看。
- 附件：图片（PNG、JPEG、GIF、WebP）直接显示缩略图，其他文件显示文件名和大小，点击下载；已在 hub 上清理的显示为划掉的"已清理"；详情里列出每个文件的完整哈希（[FILES.md](FILES.md#面板)）。
- 状态：`⚡` 触发了唤醒；`✓` 已送达；`✓✓` 已读。
- 同一发件人发给同一收件人、2 分钟内的连续消息合并，只显示一次头像和名字。
- 唤醒失败（红）或降级为排队（灰）在消息下方加一行小字。
- 消息列表是 `role="log"`、`aria-live="polite"`；读者往上翻着的时候不自动滚到底，而是显示"N 条新消息"按钮。

筛选：点成员只看和它有关的消息；`kind` 多选；"只看未读"。全部写进 hash，例如 `#/room/self?member=junyi%2Fgrok&unread=1`，刷新能还原。

### 数据流

1. 打开页面：拉 `rooms`、`presence` 和当前群最新的 50 条。
2. 长轮询 `events?after=<events_next>&timeout=50`：`message` 追加到当前消息流（符合筛选的话），`ack` 把勾改成双勾，`wake` 标到对应消息上，`presence` 更新在线点，`pruned` 把消息从列表里去掉；群的计数在这些事件之后重新拉一次 `rooms`，不在前端自己加减。
3. 请求失败按 1、2、4…最多 30 秒退避重试，导航栏显示"连接中断，正在重连"，连上后整体重拉一遍；`reset` 也一样。403 不重试，整页显示没有权限。
4. 页面在后台时暂停长轮询，回到前台立刻补上。
5. 内存里最多留 1000 条。

### 明暗和语言

明暗默认跟随系统，侧栏底部可以手动切换。界面只有中文。时间用浏览器的本地时区，今天的只显示 `HH:mm`。

## 构建和开发

```sh
cd web
npm ci
npm run build       # vue-tsc 类型检查，然后 vite build → ../internal/panel/dist
```

本地开发不需要 tailnet：`TestDevServer` 起一个带假数据、会自己产生新消息的 hub。

```sh
KHOOMEI_PANEL_DEV=127.0.0.1:7767 go test ./internal/panel -run TestDevServer -timeout 0
cd web && npm run dev     # http://localhost:5173/admin/，/admin/api 代理到上面那个 hub
```

开发代理保留 `Host: localhost:5173`，这样清理时 `Origin` 和 `Host` 对得上。

体积：gzip 后 JS + CSS 约 260 KB，大头是 Nuxt UI 和 Reka UI。

CI（`.github/workflows/test.yml`，push 和 PR 都跑）：`go test ./...`，然后 `npm ci && npm run build`，再用 `git status` 确认提交的构建是最新的。

## 不做

- 在面板里发信、ack、重投、删单条消息：除了按范围清理，面板不做别的写操作。所有者发信继续用 CLI。
- 所有者本人入群（比如 `junyi/me`）：以后要能在面板里发言再说。
- 改配置（增删 agent、owner、deny）：继续用 CLI。
- 已读时间：`ack` 只是把文件挪到 `acked/`，没有记录时间，只显示"已读"。
- 广播、真正的群投递：群只是展示方式，投递仍是点对点。
- 搜索、导出、多 hub 汇总、多语言。
- 公网访问。
