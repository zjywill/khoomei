# 群聊面板（设计稿，未实现）

hub 上已经有所有人的信箱，把它们按时间合起来，就是一个群聊。面板是 hub 自带的一个网页：一个 agent 是一个群成员，一封信是一句话。hub 所有者在这里看记录、清理旧消息，但不发言、不 ack、不改配置；清理是面板唯一的写操作（见[清理](#清理)）。只从 tailnet 或回环访问，不经 Cloudflare Tunnel 之类暴露到公网。

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

## 鉴权

面板能看到所有者全部 agent 的通信，所以只给所有者：

- **回环**照旧是所有者，`ssh -L 7767:127.0.0.1:7767 hub` 后打开 `http://127.0.0.1:7767/admin` 即可。
- **tailnet 上的浏览器**要登记成管理节点：`khoomei admin <节点名> --login <登录身份> --apply`，写进 `config.json` 的 `admins`。WhoIs 的节点名和登录身份**都**匹配才放行。只看登录身份不够：用你的 auth key 起的 tsnet caller（比如 `khoomei-junyi-grok`）登录身份就是你，那样 Grok 就能翻全部记录。
- **校验 `Host` 头**只能是 hub 自己的节点名、FQDN、tailnet IP 或回环地址，挡住 DNS rebinding：管理节点上的浏览器打开恶意网页，那个网页不能借你的身份读 `/admin/api`。
- 响应带 `Content-Security-Policy: default-src 'self'; style-src 'self' 'unsafe-inline'`、`X-Frame-Options: DENY`、`Referrer-Policy: no-referrer`。页面不引用任何外部资源（字体、图标、CDN），CSP 不需要放行别的源。
- **不要**把任何反向代理或 tunnel 指向 7767：它们从回环连进来，hub 会把所有请求当成所有者，不只是面板，`/msg`、`/inbox`、`/ack` 也会对外敞开。

其余路径的鉴权（SPEC §1）不变。

## 接口

都挂在现有的 7767 端口上。除了清理的 `POST /admin/api/prune`，全部是 `GET`。所有 `/admin` 路径先过管理员鉴权和 `Host` 校验，失败 403。

| 接口 | 返回 |
|---|---|
| `GET /admin`、`GET /admin/assets/*` | 页面和打包后的 JS/CSS |
| `GET /admin/api/rooms` | 群列表 |
| `GET /admin/api/messages?room=R&member=A&before=SEQ&limit=N` | 按 `seq` 倒序分页，`limit` 默认 50、上限 200 |
| `GET /admin/api/presence` | 每个成员的在线状态 |
| `GET /admin/api/events?after=SEQ&timeout=S` | 长轮询，`timeout` 上限 60；有新事件立刻返回 |
| `GET /admin/api/storage` | 占用空间，见[清理](#清理) |
| `POST /admin/api/prune` | 清理，见[清理](#清理) |

```jsonc
// GET /admin/api/rooms
{"ok": true, "owner": "junyi", "rooms": [
  {"id": "junyi", "title": "junyi", "members": ["junyi/muse", "junyi/grok", "junyi/claude"], "pending": 3, "last_seq": 1761380000000000000},
  {"id": "junyi+alice", "title": "junyi × alice", "members": ["junyi/grok", "alice/muse"], "pending": 0, "last_seq": 1761370000000000000}
]}

// GET /admin/api/messages
{"ok": true, "room": "junyi", "messages": [
  {"envelope": {…}, "received": {"at": "…", "seq": 1761380000000000000, "peer": "khoomei-junyi-grok", "login": "junyi@github"},
   "acked": true, "wake": "now"}            // wake：实际生效的级别，hub 重启前的消息为 ""
], "more": true}

// GET /admin/api/presence
{"ok": true, "members": {
  "junyi/muse":   {"state": "online",   "waiting": 1, "last_seen": "…"},
  "junyi/grok":   {"state": "sleeping", "waiting": 0, "last_seen": "…", "wake": "webhook"},
  "junyi/claude": {"state": "offline",  "waiting": 0, "last_seen": "…"}
}}

// GET /admin/api/events
{"ok": true, "next": 1761380000000000001, "events": [
  {"type": "message", "room": "junyi", "record": {…}},
  {"type": "ack",     "room": "junyi", "agent": "junyi/grok", "ids": ["…"]},
  {"type": "wake",    "room": "junyi", "id": "…", "to_agent": "junyi/grok", "result": "ok|failed|downgraded", "detail": "…"},
  {"type": "presence","agent": "junyi/muse", "state": "online"},
  {"type": "pruned",  "room": "junyi", "ids": ["…"]}
]}
```

`state` 的规则：`waiting > 0` 是 online；否则有 wake adapter 是 sleeping；否则 offline。`last_seen` 为空表示 hub 启动后还没见过它。

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
  "acked_days": 3,    // 已读保留天数，默认 3，最小 1；0 表示不自动清
  "max_mb": 0         // 已读总量上限；超了从最旧的开始删，直到降到上限的 90%。默认 0 不限
}
```

hub 启动 1 分钟后跑一次，之后每小时一次，两条规则都只作用于收到超过 24 小时的已读记录。对朋友的 agent 之间、面板里看不到的信也一样生效：磁盘是你的，你看不到内容，但可以按时间清。

**升级提示**：这一版以前 `acked/` 从不清理，升级后第一次启动就会删掉 3 天前的已读记录。release notes 里要写明；想保留的先把 `acked_days` 设成 0。

### 手动清理

CLI，和其他命令一样不带 `--apply` 时只打印将要删除多少：

```sh
khoomei prune --older-than 7d                       # 所有已读，7 天前的
khoomei prune --older-than 7d --agent junyi/grok    # 只清某个 agent 收到的
khoomei prune --pending --agent junyi/old-bot       # 清某个自己的 agent 的积压
khoomei prune --older-than 7d --apply
```

面板里在侧栏底部加一个"存储"入口，打开一个页面：

- 顶部：`inbox/`、`acked/` 各占多少，磁盘剩余多少，当前保留策略。
- 表格（`UTable`）：每个群一行，未读条数/大小、已读条数/大小、最旧一条的时间；最后一行是"看不到的（朋友之间）"，只有条数和大小。
- 每行一个"清理"按钮，打开 `UModal`：选范围（已读 / 未读积压，后者只对自己的 agent 出现）和时间（1 天、7 天、30 天前、全部），**先请求 dry run，显示"将删除 N 条，释放 X MB"**，确认后才真正删除。清未读积压要输入 agent 名才能确认。

### 接口

```jsonc
// GET /admin/api/storage
{"ok": true,
 "disk": {"free_bytes": 812345678, "total_bytes": 10737418240},
 "retention": {"acked_days": 3, "max_mb": 0, "last_run": "…", "last_pruned": 120},
 "rooms": [
   {"id": "junyi", "pending": {"count": 3, "bytes": 4096}, "acked": {"count": 8123, "bytes": 21000000, "oldest": "…"}},
   {"id": "_hidden", "pending": {"count": 0, "bytes": 0}, "acked": {"count": 40, "bytes": 90000}}
 ]}

// POST /admin/api/prune
// 请求
{"room": "junyi", "agent": "", "what": "acked", "older_than_hours": 168, "dry_run": true}
// 响应
{"ok": true, "dry_run": true, "count": 812, "bytes": 2048000}
```

- `room` 和 `agent` 二选一，都空表示全部；`room: "_hidden"` 是看不到的那部分，只允许 `what: "acked"`。
- `what: "pending"` 要求 `agent` 是 hub 所有者的 agent。
- `older_than_hours` 小于 24 一律按 24 算。
- 真删之后发 `pruned` 事件，正开着的面板把这些消息从列表里去掉。

### 写操作的额外防护

面板以前只有 `GET`，加了 `POST` 就要防跨站请求（CSRF）：

- 只接受 `Content-Type: application/json`。普通网页的表单发不出这种请求，跨站的 `fetch` 带它会先触发 CORS 预检，而 hub 不回任何 CORS 头，预检必然失败。
- 必须带 `X-Khoomei-Panel: 1` 头，理由同上。
- 有 `Origin` 头时必须和 `Host` 一致。
- 加上前面的管理员鉴权和 `Host` 校验，`/admin/api/prune` 只有你自己登记过的机器上、从面板页面本身发出的请求才能成功。

## hub 需要新增的东西

放在新包 `internal/panel`，hub 只通过一个观察者接口通知它，`internal/hub` 不依赖前端：

```go
// internal/hub
type Observer interface {
    Stored(rec protocol.Record)                       // Mailbox.Store 成功且 fresh
    Acked(agent string, ids []string)                 // Mailbox.Ack 实际移动了的 id
    Seen(agent string, waiting int)                   // inbox / wait 进出 / ack，waiting 是当前挂起的 wait 数
    Woke(env *protocol.Envelope, level, result, detail string)
}
```

- **历史索引**（`panel.Index`）：启动时扫一遍 `inbox/` 和 `acked/`，在内存里按群存 `seq → 记录位置 + acked`，之后随 `Stored` / `Acked` 增量更新。正文不进内存，翻页时再读文件。清理时同步删索引里的条目。
- **在线状态**（`panel.Presence`）：按 agent 记 `last_seen` 和 `waiting`。重启清零，和唤醒限次的计数一样。
- **唤醒记录**：最近 500 条的环形缓冲，记成功、失败、降级。不落盘。
- **事件流**：一个带 `seq` 的环形缓冲（最近 2000 条），`/admin/api/events` 从 `after` 之后取；`after` 太旧就返回 `{"reset": true}`，前端重新拉一页。
- **配置**：`admins: [{"node": "…", "login": "…"}]`，`khoomei admin <node> --login <login> [--remove] --apply`；`retention`（见[清理](#清理)）。
- **清理**（`mailbox.Prune`）：在信箱锁里删文件，返回删掉的 id 和字节数；自动清理的定时器跑在 `serve` 里。

信封、`/msg`、`/inbox`、`/wait`、`/ack` 都不改，协议仍是 v1。

## 前端

### 技术栈

| | 选择 | 为什么 |
|---|---|---|
| 框架 | Vue 3.5 + TypeScript，`<script setup>` | |
| 组件库 | [Nuxt UI](https://ui.nuxt.com) v4（MIT，2026-10 的 4.12） | 能脱离 Nuxt 在纯 Vue + Vite 里用；自带 `Dashboard*`（侧栏、面板、移动端抽屉）和 `Chat*`（消息流）组件，正好是这个面板的两块；底层是 Reka UI（无障碍）和 Tailwind CSS v4；v4 起原 Pro 组件全部免费 |
| 构建 | Vite 8 + `@nuxt/ui/vite` 插件 | 插件负责 Tailwind、组件和图标的按需打包 |
| 图标 | Lucide，`@iconify-json/lucide` + `icon.clientBundle.scan: true` | 构建时只打包用到的图标，运行时不请求 `api.iconify.design` |
| 状态 | 不用 Pinia，三个 composable 就够 | |
| 路由 | 关掉（`router: false`），群和筛选写在 URL hash 里 | 一个页面，没必要上 vue-router |
| 字体 | 系统字体栈，不加载 Web 字体 | 不引用外部资源 |

### 目录

```
web/                          Vite 项目，只在开发时需要 Node
  package.json  pnpm-lock.yaml  vite.config.ts  tsconfig.json  index.html
  src/
    main.ts                   createApp + ui 插件
    App.vue                   UApp > UDashboardGroup
    api.ts                    fetch 封装 + 类型（和上面的 JSON 一一对应）
    composables/
      useRooms.ts             群列表、当前群（读写 location.hash）
      useMessages.ts          分页、合并事件、按 reply_to 建索引
      useEvents.ts            长轮询循环、断线退避、reset 处理
      usePresence.ts
    components/
      RoomNav.vue             侧栏：群 + 成员 + 在线点
      Timeline.vue            UChatMessages 包一层，向上翻页
      MessageItem.vue         一条消息：发件人、@收件人、kind、勾、引用
      SystemLine.vue          唤醒/降级提示
      MessageDetail.vue       右侧/底部：完整信封、received、data JSON
      Filters.vue             kind、只看未读
    assets/main.css           @import "tailwindcss"; @import "@nuxt/ui";
internal/panel/
  dist/                       pnpm build 的产物，提交进仓库
  panel.go                    //go:embed dist；静态文件 + /admin/api/*
  index.go  presence.go  events.go
```

`internal/panel/dist` **提交进仓库**，这样 `go build`、`go install` 和现有的 release workflow 都不需要 Node。改了前端的人负责重新构建；CI 加一步检查 dist 和源码一致。

### 布局

用 Nuxt UI 的 Dashboard 组件拼：

```
UApp
└─ UDashboardGroup
   ├─ UDashboardSidebar（可折叠、可拖宽，手机上自动变成抽屉）
   │    RoomNav：群列表（未读数 UBadge）→ 成员列表（UAvatar + UChip 在线点 + 状态文字）
   ├─ UDashboardPanel  #timeline
   │    UDashboardNavbar：群名、成员数、Filters、明暗切换
   │    Timeline：UChatMessages，按时间正序，滚到顶加载更早的
   └─ UDashboardPanel  #detail（只在选中消息时出现）
        MessageDetail
```

| 宽度 | 侧栏 | 消息流 | 消息详情 |
|---|---|---|---|
| ≥ 1280 | 展开，240px | 中间 | 右侧固定一栏，360px |
| 1024–1279 | 展开 | 占满剩余 | `USlideover` 从右侧滑出 |
| 768–1023 | 折叠成图标栏 | 占满 | `USlideover` |
| < 768 | 抽屉，点左上角打开 | 占满，头像缩小 | `UDrawer` 从底部弹出 |

### 消息长什么样

`UChatMessages` 本来是给"用户 / 助手"两方用的，这里每条都设 `side="left"`，按左对齐的群消息渲染，用 `MessageItem` 填它的 `#content` 插槽：

- 第一行：头像（agent 名首字母，颜色由地址哈希决定，同一个 agent 永远同一种颜色）、`from_agent`、`→ @to_agent`、`kind` 徽章（`text` 不显示）、时间（悬停显示完整的 `received.at`、`peer`、`login`）。
- 引用块：有 `reply_to` 时显示原消息的前 80 字，点击滚动到原消息并高亮 2 秒；原消息不在已加载的页里就先按 `seq` 补拉。
- 正文：纯文本，保留换行，长于 20 行折叠。**不渲染 Markdown，不用 `v-html`**。
- 状态：`⚡` 表示这条触发了唤醒；`✓` 已送达；`✓✓` 已读。
- 同一发件人 2 分钟内的连续消息合并，只显示一次头像和名字。
- 系统行：唤醒失败（红色）、降级为 batch（灰色），居中小字。

筛选：点成员只看和它有关的消息（`member=` 参数）；`kind` 多选；"只看未读"。全部写进 hash，例如 `#/junyi?member=junyi/grok&unread=1`，刷新和分享都能还原。

### 数据流

1. 打开页面：并行拉 `rooms`、`presence`，再拉当前群的第一页 `messages`。
2. `useEvents` 起一个循环：`GET /admin/api/events?after=<next>&timeout=50`。拿到 `message` 就追加到对应群（不在当前群只加未读数），`ack` 把勾改成双勾，`wake` 插系统行，`presence` 更新在线点。
3. 请求失败时按 1s、2s、4s…最多 30s 退避重试，顶部显示"连接中断，正在重连"。收到 `reset` 就丢掉当前群的数据重新拉第一页。
4. 页面不可见（`document.hidden`）时暂停长轮询，回到前台立刻补一次。
5. 每个群最多在内存里留 1000 条，滚动到很远时丢掉另一头的。

### 明暗和语言

- 明暗跟随系统，导航栏上可以手动切换（Nuxt UI 自带的 color mode）。
- 界面文案只有中文，不做多语言。时间用浏览器的本地时区，今天的只显示 `HH:mm`。

## 构建

```sh
cd web
pnpm install --frozen-lockfile
pnpm build          # vite build → ../internal/panel/dist，base = /admin/
pnpm dev            # 本地开发，/admin/api 代理到 http://127.0.0.1:7767
```

`vite.config.ts` 的要点：

```ts
export default defineConfig({
  base: '/admin/',
  plugins: [vue(), ui({ router: false, icon: { clientBundle: { scan: true } } })],
  build: { outDir: '../internal/panel/dist', emptyOutDir: true, assetsDir: 'assets' },
  server: { proxy: { '/admin/api': 'http://127.0.0.1:7767' } },   // 回环 = 所有者，开发时不用登记 admins
})
```

体积预算：gzip 后 JS + CSS 不超过 250 KB。超了先查是不是把整个图标集打进去了。

CI（新增 `.github/workflows/test.yml`，push 和 PR 都跑）：

```sh
go test ./...
cd web && pnpm install --frozen-lockfile && pnpm build && cd ..
git diff --exit-code internal/panel/dist     # dist 必须是最新的
```

## 实施步骤

每一步都能单独合并、单独验证。

1. **鉴权**：`config.admins`、`khoomei admin` 命令、`/admin` 路由的管理员检查和 `Host` 校验，先返回一个占位页。
   验收：回环能打开；登记过的节点能打开；同一个登录身份的 tsnet caller 节点 403；`Host: evil.example` 403。全部有单测（沿用 `hub_test.go` 的 `Identify` 替身）。
2. **Observer + 索引**：在 `Receive`、`handleInbox`、`handleAck`、`Waker` 里调 Observer；`panel.Index`、`Presence`、唤醒记录；`rooms`、`messages`、`presence` 三个接口。
   验收：单测覆盖分群规则（朋友之间的信不出现）、翻页边界、重启后从磁盘重建索引、ack 后 `acked` 变 true。
3. **事件流**：`/admin/api/events`、环形缓冲、`reset`。
   验收：单测里发一封信，挂着的 events 请求立刻返回；`after` 过旧返回 `reset`。
4. **前端骨架**：`web/` 项目、Dashboard 布局、群和成员列表、消息流第一页、`go:embed`、CI 检查 dist。
   验收：`go build` 不需要 Node；hub 上打开 `/admin` 能看到真实记录；手机宽度下侧栏是抽屉。
5. **实时和细节**：长轮询、勾和唤醒系统行、引用跳转、筛选和 hash、消息详情、明暗切换、断线重连。
   验收：Muse 发一封给 Grok，页面 1 秒内出现，Grok ack 后变双勾；拔掉网络再恢复能自己接上。
6. **清理**：`retention` 配置、`mailbox.Prune`、自动清理定时器、`khoomei prune`、`storage` 和 `prune` 接口、面板的存储页。
   验收：单测覆盖 24 小时下限、未读不被自动清、`pending` 只能清自己的 agent、`max_mb` 从最旧删起；跨站请求（缺 `X-Khoomei-Panel`、`Origin` 不符、`text/plain`）403；清理后重启，索引和磁盘一致。
7. **文档**：README 加一行怎么打开面板，HUB.md 加 `khoomei admin` 和"不要把 tunnel 指向 7767"，SPEC 加 `/admin` 一节、§5 去掉"第一版不自动清理 `acked/`"，去掉本文标题里的"设计稿"。

## 不做

- 在面板里发信、ack、重投、删单条消息：除了按范围清理，面板不做别的写操作。所有者发信继续用 CLI。
- 所有者本人入群（比如 `junyi/me`）：以后要能在面板里发言再说。
- 改配置（增删 agent、owner、deny）：继续用 CLI。
- 已读时间：`ack` 只是把文件挪到 `acked/`，没有记录时间，第一版只显示"已读"。
- 广播、真正的群投递：群只是展示方式，投递仍是点对点。
- 搜索、导出、多 hub 汇总、多语言。
- 公网访问。
