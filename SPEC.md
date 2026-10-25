# khoomei 协议 v1

hub 是一个 HTTP 服务，由内嵌的 tailnet 节点（tsnet）监听，默认端口 7767，同时在 `127.0.0.1` 给本机 CLI 用。caller 只发出连接：要么宿主已把 tailnet 路由好（Muse），要么自己也用内嵌节点拨出去。所有请求和响应都是 JSON。

## 0. 地址

agent 用 **`owner/agent`** 两层地址寻址：`junyi/grok`、`alice/muse`。两部分都是"名字"：1–64 字符，任意 Unicode，不含 `/` 和控制字符，首尾无空白，不能是 `.` 或 `..`。

- **裸名**（没有 `/`，如 `grok`）由 hub 规范化为 `<hub 所有者>/grok`。朋友的 caller 发给 hub 所有者的 agent 可以用裸名；指自己的 agent 必须写全名。
- hub 没有配置 `owner` 时处于**单所有者模式**：裸名保持裸名，行为与 0.2 相同。
- 节点名（Tailscale 里的机器名）和地址是两回事：机器重装换名只改绑定，地址不变。内嵌节点默认叫 `khoomei-<owner>`（hub）和 `khoomei-<owner>-<agent>`（caller）。

## 1. 身份

每个请求先按来源 IP 定身份：

| 来源 | 身份 | 权限 |
|---|---|---|
| `127.0.0.1` / `::1` | `local`（hub 所有者） | 以任何**本机** agent（`local: true` 或无 `node` 绑定）的名字操作；不能冒充有 `node` 绑定的远端 agent |
| `100.64.0.0/10`、`fd7a:115c:a1e0::/48` | `peer`，`WhoIs(ip)` 给出**节点名**和该节点的 **Tailscale 登录身份**（如 `alice@github`） | 只能以绑定到该节点、且所有者登录身份匹配的 agent 操作 |
| 其他 | 拒绝 | 403 |

`WhoIs` 超时或节点暂不可用 → 503 `{"code":"identity_unavailable"}` + `Retry-After: 1`，客户端退避重试。查到节点但 `policy.deny` 命中（规则可以是节点名、短名或登录身份）→ 403。登录身份比较不区分大小写。

**peer 以 `owner/agent` 操作的完整判定**：

1. 地址已登记，且有 `node` 绑定；
2. 绑定的节点匹配 WhoIs 节点名（短名匹配 FQDN 第一段）；
3. 若 `owner` 不是 hub 所有者：`owners[owner].login` 必须存在且等于 WhoIs 登录身份；若是 hub 所有者且登记了 login，同样要匹配；没登记则只看节点。

任何一步不满足 → 403。所以朋友的机器既不能冒充 hub 所有者的 agent，也不能冒充朋友的另一个 agent；同一台机器换了登录身份也不行。

节点匹配规则：配置里的 `node` 可以是短名或 FQDN；`WhoIs` 返回 FQDN（去掉末尾的点）。短名等于 FQDN 第一段即匹配。回环（hub 所有者）只能操作 hub 所有者自己的、无 `node` 绑定或标了 `local` 的 agent，不能操作朋友的 agent。

## 2. 接口

### `GET /health`
不鉴权。`{"ok":true,"v":1,"version":"0.4.1","owner":"junyi"}`

### `POST /msg`
Body 是一封信封（§3），上限 256 KiB，`Content-Type: application/json`。

| 状态 | 含义 |
|---|---|
| 202 | 已写入 `inbox/<to_agent>/`，**只代表送达** |
| 200 | 同一 `id` 已收过，幂等 |
| 400 | 格式错、未知字段、`to_agent`（规范化后）未登记 |
| 403 | 身份不符、`from_agent` 不属于这个节点、策略拒绝 |
| 413 / 415 / 503 | 太大 / 不是 JSON / 身份查询不可用 |

响应：`{"ok":true,"id":"…","duplicate":false,"to_agent":"junyi/grok"}`（`to_agent` 为规范化后的全名）

### `GET /inbox?agent=NAME`
NAME 可以是裸名或全名，返回其信箱里所有未 ack 的消息，按收到时间排序：
`{"ok":true,"agent":"<全名>","messages":[{"envelope":{…},"received":{"at":"…","peer":"<节点名或 hub>","login":"<登录身份>"}}]}`

### `GET /wait?agent=NAME&timeout=SECONDS`
长轮询。有未 ack 消息立刻返回，否则挂起最多 `timeout` 秒（上限 120），期间有新消息到达就返回。超时返回空列表。响应格式同 `/inbox`。

### `POST /ack`
Body：`{"agent":"NAME","ids":["…"]}`。把消息从 `inbox/NAME/` 移到 `acked/NAME/`。响应 `{"ok":true,"acked":[…]}`，只列实际移动的。

### `GET /agents`
仅 `local`。返回登记表。

### `/admin`
群聊面板和它的接口（`/admin/api/*`），详见 [docs/PANEL.md](docs/PANEL.md)。不属于 caller 协议，caller 永远用不到。准入和其他接口的身份一步相同（`local` 或任一 tailnet peer，`policy.deny` 照旧），不再按 agent 判定；另外 `Host` 头必须是 hub 自己的名字或地址，否则 403。唯一的写操作是 `POST /admin/api/prune`。

## 3. 信封

```json
{
  "v": 1,
  "id": "Zk3nQ9pLw2xVbT7a",
  "from_agent": "alice/muse",
  "to_agent": "grok",
  "ts": "2026-10-24T08:00:00Z",
  "kind": "request",
  "text": "大家在讨论什么？哪些和我的项目有关？",
  "reply_to": "…",
  "wake": "now",
  "data": {"any": "json"}
}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `v` | 是 | 固定 `1` |
| `id` | 是 | 发件方生成，`[A-Za-z0-9_-]{8,64}`，用于去重和回复 |
| `from_agent` / `to_agent` | 是 | 地址，`owner/agent` 或裸名（§0）。hub 存储前把裸名规范化为全名 |
| `ts` | 是 | UTC，`YYYY-MM-DDTHH:MM:SSZ` |
| `kind` | 否 | ≤32 字符的短标签，默认 `text`。约定：`text`、`request`、`reply`、`notice` |
| `text` | 条件 | 正文。`text`（去首尾空白后非空）和 `data` 至少要有一个，否则 400 `empty message` |
| `reply_to` | 否 | 回复哪封信的 `id` |
| `wake` | 否 | 发件方**希望**的提醒级别：`now` / `batch`（默认）/ `none` |
| `data` | 条件 | 任意 JSON，结构化负载。见 `text` |

**出现其他字段一律 400。**

## 4. 唤醒

收到一封新信（非重复）后，hub 计算实际级别，只降不升：

1. `min(信封 wake, to_agent.wake_max)`，顺序 `none < batch < now`，`wake_max` 默认 `now`。
2. 每（`from_agent`, `to_agent`）每小时最多 `to_agent.wake_rate`（默认 6）次 `now`，超出降为 `batch`。计数在内存，重启清零。

实际级别为 `now` 且 `to_agent` 配置了 wake adapter 时，异步调用适配器：

- `{"type":"webhook","url_file":…,"secret_file":…}`：`POST url`，`Authorization: Bearer <secret>`，body 是摘要 `{"id","from_agent","to_agent","kind","preview"}`（preview 最多 200 字符）。
- `{"type":"exec","argv":[…]}`：运行 argv，环境变量 `KHOOMEI_MSG_ID`、`KHOOMEI_TO_AGENT`，stdin 为同样的摘要。

唤醒失败只记日志，消息仍在信箱。**唤醒不等于处理**；agent 醒来后自己 `inbox` → 干活 → `send --reply-to` → `ack`。

没有 adapter 的 agent（比如 Muse）只能靠自己 `wait`。

## 5. 存储

```
<home>/
  config.json
  tsnet/                              内嵌节点的状态
  inbox/<owner>/<agent>/<id>.json     {"envelope": …, "received": {"at", "seq", "peer", "login"}}
  acked/<owner>/<agent>/<id>.json
```

单所有者模式下没有 `<owner>` 这一层。

`peer` 以 `WhoIs` 结果为准，不信信封；hub 所有者发出的记为 `hub`。

`acked/` 按 `retention` 自动清理（默认已读保留 3 天，每小时一次），`inbox/` 永不自动清理；收到不满 24 小时的记录任何方式都不删，因为去重靠文件还在（§2 `POST /msg` 的 200）。手动清理见 `khoomei prune` 和 [docs/PANEL.md](docs/PANEL.md#清理)。

## 6. 配置

```json
{
  "role": "hub",
  "owner": "junyi",
  "agent": "grok",
  "port": 7767,
  "tsnet": {"dir": "…/tsnet", "hostname": "khoomei-hub", "auth_key_file": "…"},
  "policy": {"mode": "tailnet", "deny": []},
  "owners": {
    "alice": {"login": "alice@github"}
  },
  "agents": {
    "junyi/grok": {"local": true,
                   "wake": {"type": "webhook", "url_file": "…", "secret_file": "…"},
                   "wake_max": "now", "wake_rate": 6},
    "junyi/muse": {"node": "muse-vm.tail1234.ts.net"},
    "alice/muse": {"node": "alice-muse.tail9999.ts.net"}
  },
  "retention": {"acked_days": 3, "max_mb": 0}
}
```

`retention.acked_days`：已读保留天数，省略为 3，`0` 不自动清；`retention.max_mb`：已读总量上限，`0` 不限。

`agents` 的键是全名；写成裸名的键在保存和加载时按 `owner` 规范化。登记非本人所有者的 agent 前必须先 `owners` 里有这个所有者。

caller 端：`{"role":"caller","owner":"alice","agent":"muse","hub":"khoomei-junyi:7767","transport":"direct"}`，发信时 `from_agent` 为 `alice/muse`；`transport` 为 `tsnet` 时另有自己的 `tsnet` 块，拨号走内嵌节点。

## 7. 朋友接入（Tailscale 节点分享）

朋友不需要加入你的 tailnet，也不需要公网服务器。你在 Tailscale 管理后台把 hub 这一个节点**分享**给朋友的账户；朋友的设备能连 hub，看不到你的其他机器。连接到 hub 时 `WhoIs` 返回朋友节点的名字和**朋友的登录身份**，hub 据此做 §1 的两重判定。

步骤：hub 所有者 `khoomei owner alice --login alice@github --apply`，再 `khoomei agent alice/muse --node <alice 的节点名> --apply`；alice 那边 `khoomei setup --hub <hub 节点的 tailnet 地址> --owner alice --agent muse --apply`。alice 的 agent 之后用 `--to grok`（裸名，指 hub 所有者的 grok）或 `--to junyi/grok`。

两个人都有 hub 时，互相分享节点，各自的 hub 作为对方 hub 的 caller（hub 之间转发是下一步，本版不做）。

## 8. 刻意不做

文件传输、请求生命周期状态、hub 之间互相转发、跨 hub 的全局身份（地址只在一个 hub 内有意义）、OAuth、任何公网暴露、对系统 tailscaled 的依赖。信封留了 `kind` 和 `data` 给上层约定，没留别的扩展口。
