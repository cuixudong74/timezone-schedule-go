# timezone-schedule-go

跨时区的周期日程与提醒调度引擎。用户按自己所在的时区创建**每周**或**每月**的周期日程，系统据此算出每一次触发的 UTC 时刻，并在夏令时（DST）切换导致的本地时间不规则场景下给出稳定、可解释的结果。

## 1. 时间模型

### 1.1 墙钟时间 vs UTC 时刻

- **墙钟时间（Wall Time）**：日历上的 `年-月-日 时:分:秒`，不带时区。例如"纽约时间每周二 10:00"里的 `10:00`。
- **UTC 时刻（Instant）**：真实发生的绝对时刻，带时区偏移。

日程规则在**墙钟时间**坐标系中定义（"每周二 10:00"），引擎把每个墙钟时间解析为一个 UTC 时刻后持久化。一旦解析完成，存储的就是 UTC 时刻，**不再随时区规则变化而重算**——这是历史稳定性的基础。

### 1.2 为什么不同机器结果必须一致

触发序列只依赖于：规则（星期几/每月几日、时分秒）+ 目标时区的 IANA 规则。引擎不读取机器默认时区（`time.Local`），所有计算都在显式指定的 `*time.Location` 上进行。因此同一日程在任何机器、任何默认时区下算出的 UTC 序列完全一致。

### 1.3 夏令时：重复与缺失

DST 切换会产生两种不规则本地时间，引擎**显式识别**而不是静默顺延：

| 场景 | 成因 | 本地时间 | 处理 |
|------|------|----------|------|
| **重复** | 秋天拨回（fall back） | 同一个墙钟时间出现两次 | 至多触发一次，二选一 |
| **缺失** | 春天前拨（spring forward） | 某个墙钟时间不存在 | 跳过 / 前顺 / 后顺 |

**重复时刻**（如纽约 2024-11-03 01:30）：引擎在解析时刻前后各 72 小时内收集候选 UTC 偏移，逐一回环验证，得到两个合法时刻。策略：

- `first`（默认）：取第一次（较早的 UTC 时刻，如 05:30 UTC）。
- `last`：取第二次（较晚的 UTC 时刻，如 06:30 UTC）。

**缺失时刻**（如纽约 2024-03-10 02:30）：引擎通过墙钟分量比对识别。策略：

- `skip`（默认）：跳过这一次，不触发。
- `shift-forward`：顺延到缺口之后第一个存在的时刻（03:30 → 07:30 UTC）。
- `shift-backward`：回退到缺口之前最后一个存在的时刻（01:30 → 06:30 UTC）。

每个触发记录都带有 `resolution` 字段，用自然语言说明它是正常、重复（选了第一次/最后一次）还是缺失（跳过/前顺/后顺），做到**可解释**。

### 1.4 月末日期

`day 31` 这类规则在小月（2 月、4 月…）不存在。策略：

- `skip`（默认）：跳过没有该日的月份。
- `clamp`：钳制到该月最后一天。

## 2. 日程修改与取消

### 2.1 生效边界确定

修改（Update）时指定 `effective_at`（默认为当前时间）。边界语义：

- `scheduled_at < effective_at` 的触发 = **历史**，不可变，保留旧规则结果。
- `scheduled_at >= effective_at` 且**尚未投递**的触发 = **未来**，删除后按新规则重算。
- 已投递的触发无论边界如何都保留（历史不可篡改）。

若 `effective_at` 被设为过去时间，则 `[effective_at, now)` 窗口内的新规则触发属于过去，**不再补发**（它们已由旧规则管辖），并在报告中给出 warning。

每次修改生成一个新的 revision（不可变记录），记录新规则、生效边界、规则版本、操作人与原因。

### 2.2 取消

取消（Cancel）把日程置为 `cancelled`，删除所有尚未投递的未来触发，保留已投递历史。

### 2.3 序号不复用

每个日程有 `next_seq` 计数器。修改后新触发的序号从历史最大序号之后继续，**永不复用**被删除触发的序号，保证审计可追溯。

## 3. 时区规则升级

- 系统内嵌 IANA tzdata（`time/tzdata`），二进制可独立运行。
- 也支持从目录加载 zoneinfo（`ReloadableProvider`），运行期可 `Reload()`。
- 每个触发记录 `rules_version`（解析时的规则版本）。
- **历史触发一旦生成为 UTC 时刻并落盘，规则升级不会重算或篡改它们**。升级后只有新生成的触发使用新规则。
- 规则版本通过目录下的 `VERSION` 文件标识；无此文件时用目录内容哈希作为版本，任何 zoneinfo 变更都可被观测到。

## 4. 提醒投递：幂等 + 崩溃恢复

投递采用 **outbox 模式**，保证至少一次（at-least-once）且用户视角恰好一次：

- 每个触发有状态机：`planned → delivering → delivered`，失败则 `failed`（带退避重试）。
- Worker 周期性 `ClaimDue`：原子地领取到期触发（置 `delivering` + 租约）。
- 投递前先查投递记录；投递成功后写投递记录（`PutDelivery` 幂等插入），再标记 `delivered`。
- **幂等键**：`(schedule_id, seq)`。投递服务必须对该键幂等——即使因崩溃重试而重复调用，也不会产生两条提醒。
- **租约恢复**：Worker 崩溃后租约到期，其他（或重启后的）Worker 通过 `ReclaimExpired` 重新领取；投递记录的幂等性保证不会重复。
- 失败重试带指数退避；超过 `max_attempts` 后置为死信（不再领取）。

## 5. 批量重排

`BatchReschedule` 在调度器互斥锁内串行应用一批修改，因此：

- **期间到达的新增/修改请求不丢失**：它们在锁外排队，批次提交后逐个应用。
- 每个条目独立成功或失败，结果包含 `kept_seqs` / `removed_seqs` / `added`，可逐条对齐、可解释。
- 越权条目返回 `action=error` 与原因，不影响其他条目。

## 6. 授权与审计

- 每个请求带 `X-User-Id` 头；缺失 → `401`。
- 日程按 `owner_id` 隔离：访问/修改/取消他人日程 → `403`，错误码 `unauthorized`。
- 非法时区标识 → `400`，错误码 `invalid_timezone`。
- 非法规则（如没有星期几、时间越界）→ `400`，错误码 `invalid_recurrence`。
- 所有写操作与越权尝试写入审计记录（操作人、动作、对象、结果、原因、时间）。

## 7. 配置项

通过环境变量配置（均有默认值）：

| 变量 | 默认 | 说明 |
|------|------|------|
| `TZ_SCHED_ADDR` | `:8080` | HTTP 监听地址 |
| `TZ_SCHED_DB` | （空） | bbolt 数据库路径；为空用内存存储 |
| `TZ_SCHED_TZDIR` | （空） | zoneinfo 目录；为空用内嵌 tzdata |
| `TZ_SCHED_TZ_VERSION` | （空） | 内嵌规则版本标签覆盖 |
| `TZ_SCHED_HORIZON_DAYS` | `400` | 触发预生成窗口（天） |
| `TZ_SCHED_MAX_OCCURRENCES` | `2000` | 单次生成上限 |
| `TZ_SCHED_WORKER_ENABLED` | `true` | 是否启动投递 Worker |
| `TZ_SCHED_WORKER_INTERVAL` | `1s` | Worker 轮询间隔 |
| `TZ_SCHED_LEASE_SECONDS` | `30` | 投递租约时长 |
| `TZ_SCHED_MAX_RETRIES` | `5` | 最大重试次数 |
| `TZ_SCHED_RETRY_BACKOFF` | `10s` | 重试退避基数 |
| `TZ_SCHED_DST_REPEATED` | `first` | 重复时刻策略：`first`/`last` |
| `TZ_SCHED_DST_MISSING` | `skip` | 缺失时刻策略：`skip`/`shift-forward`/`shift-backward` |
| `TZ_SCHED_MONTH_DAY_OOB` | `skip` | 月末无日策略：`skip`/`clamp` |

## 8. HTTP API

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/healthz` | 健康检查 |
| POST | `/api/schedules` | 创建日程 |
| GET | `/api/schedules` | 列出我的日程 |
| GET | `/api/schedules/{id}` | 获取日程 |
| PATCH | `/api/schedules/{id}` | 修改日程 |
| POST | `/api/schedules/{id}/cancel` | 取消日程 |
| POST | `/api/schedules/batch-reschedule` | 批量重排 |
| GET | `/api/schedules/{id}/triggers` | 列出触发 |
| GET | `/api/audit` | 审计记录 |

所有接口需 `X-User-Id` 头。请求/响应为 JSON。错误响应形如 `{"error": "<code>", "message": "..."}`，错误码可区分（`unauthorized` / `invalid_timezone` / `invalid_recurrence` / `not_found` / `conflict` / `internal`）。

## 9. 运行

```bash
# 内存存储
go run ./cmd/server

# 持久化 + 自定义 DST 策略
TZ_SCHED_DB=/tmp/sched.db TZ_SCHED_DST_MISSING=shift-forward go run ./cmd/server
```

## 10. 测试

```bash
go test ./...
go test -race ./...
```

测试覆盖：

- 夏令时重复/缺失时刻（纽约、悉尼、Lord Howe 30 分钟偏移）与三种策略。
- 每周/每月规则、月末 skip/clamp、跨 DST 触发序列、确定性。
- 修改生效边界、取消、历史不可变、序号不复用。
- 时区规则升级后历史稳定、新计算用新规则。
- 投递幂等、失败重试、崩溃重启恢复、多 Worker 并发。
- 批量重排、并发请求不丢失、结果对齐。
- 越权、非法时区、非法时间输入的可区分拒绝与审计。
- HTTP API 端到端。
