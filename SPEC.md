# khoomei 协议 v1

hub 是一个 HTTP 服务，由内嵌的 tailnet 节点（tsnet）监听，默认端口 7767，同时在 `127.0.0.1` 给本机 CLI 用。caller 只发出连接：要么宿主已把 tailnet 路由好（Muse），要么自己也用内嵌节点拨出去。所有请求和响应都是 JSON。

## 1. 身份

每个请求先按来源 IP 定身份：

| 来源 | 身份 | 权限 |
|---|---|---|
| `127.0.0.1` / `::1` | `local`（hub 所有者） | 以任何**本机** agent（`local: true` 或无 `node` 绑定）的名字操作；不能冒充有 `node` 绑定的远端 agent |
| `100.64.0.0/10`、`fd7a:115c:a1e0::/48` | `peer`，节点名来自内嵌节点的 `WhoIs(ip)` | 只能以绑定到该节点的 agent 的名字操作 |
| 其他 | 拒绝 | 403 |

`WhoIs` 超时或节点暂不可用 → 503 `{"code":"identity_unavailable"}` + `Retry-After: 1`，客户端退避重试。查到节点但 `policy.deny` 命中 → 403。

节点匹配规则：配置里的 `node` 可以是短名或 FQDN；`WhoIs` 返回 FQDN（去掉末尾的点）。短名等于 FQDN 第一段即匹配。

## 2. 接口

### `GET /health`
不鉴权。`{"ok":true,"v":1,"version":"0.2.0"}`

### `POST /msg`
Body 是一封信封（§3），上限 256 KiB，`Content-Type: application/json`。

| 状态 | 含义 |
|---|---|
| 202 | 已写入 `inbox/<to_agent>/`，**只代表送达** |
| 200 | 同一 `id` 已收过，幂等 |
| 400 | 格式错、未知字段、`to_agent` 未登记 |
| 403 | 身份不符、`from_agent` 不属于这个节点、策略拒绝 |
| 413 / 415 / 503 | 太大 / 不是 JSON / 身份查询不可用 |

响应：`{"ok":true,"id":"…","duplicate":false}`

### `GET /inbox?agent=NAME`
返回 NAME 信箱里所有未 ack 的消息，按收到时间排序：
`{"ok":true,"agent":"NAME","messages":[{"envelope":{…},"received":{"at":"…","peer":"<节点名或 hub>"}}]}`

### `GET /wait?agent=NAME&timeout=SECONDS`
长轮询。有未 ack 消息立刻返回，否则挂起最多 `timeout` 秒（上限 120），期间有新消息到达就返回。超时返回空列表。响应格式同 `/inbox`。

### `POST /ack`
Body：`{"agent":"NAME","ids":["…"]}`。把消息从 `inbox/NAME/` 移到 `acked/NAME/`。响应 `{"ok":true,"acked":[…]}`，只列实际移动的。

### `GET /agents`
仅 `local`。返回登记表。

## 3. 信封

```json
{
  "v": 1,
  "id": "Zk3nQ9pLw2xVbT7a",
  "from_agent": "muse",
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
| `from_agent` / `to_agent` | 是 | agent 名：1–64 字符，任意 Unicode，不含 `/` 和控制字符，首尾无空白 |
| `ts` | 是 | UTC，`YYYY-MM-DDTHH:MM:SSZ` |
| `kind` | 否 | ≤32 字符的短标签，默认 `text`。约定：`text`、`request`、`reply`、`notice` |
| `text` | 否 | 正文 |
| `reply_to` | 否 | 回复哪封信的 `id` |
| `wake` | 否 | 发件方**希望**的提醒级别：`now` / `batch`（默认）/ `none` |
| `data` | 否 | 任意 JSON，结构化负载 |

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
  inbox/<agent>/<id>.json     {"envelope": …, "received": {"at", "seq", "peer"}}
  acked/<agent>/<id>.json
```

`peer` 以 `WhoIs` 结果为准，不信信封；hub 所有者发出的记为 `hub`。第一版不自动清理 `acked/`。

## 6. 配置

```json
{
  "role": "hub",
  "agent": "grok",
  "port": 7767,
  "tsnet": {"dir": "…/tsnet", "hostname": "khoomei-hub", "auth_key_file": "…"},
  "policy": {"mode": "tailnet", "deny": []},
  "agents": {
    "grok": {"local": true,
             "wake": {"type": "webhook", "url_file": "…", "secret_file": "…"},
             "wake_max": "now", "wake_rate": 6},
    "muse": {"node": "muse-vm.tail1234.ts.net"}
  }
}
```

caller 端：`{"role":"caller","agent":"muse","hub":"khoomei-hub:7767","transport":"direct"}`；`transport` 为 `tsnet` 时另有自己的 `tsnet` 块，拨号走内嵌节点。

## 7. 刻意不做

文件传输、请求生命周期状态、hub 之间互联、OAuth、任何公网暴露、对系统 tailscaled 的依赖。信封留了 `kind` 和 `data` 给上层约定，没留别的扩展口。
