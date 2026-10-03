# timezone-schedule-go

跨时区的周期日程与提醒调度库。用户按自己所在时区创建每周/每月的周期日程，
系统计算每次触发的 UTC 时刻，并在夏令时切换等本地时间不规则场景下给出
稳定、可解释的结果。

## 结构

| 包 | 职责 |
|---|---|
| `schedule` | 周期规则、触发时刻计算、DST 间隙/重复解析 |
| `store` | 追加式事件日志（JSON Lines + fsync），重启后重放恢复 |
| `engine` | 创建/修改/取消、幂等投递、批量重排、审计 |

## 时间模型

- **本地名义时间**：规则中的触发时刻（如「每周一 09:00」）始终以日程
  所在时区（`Rule.Location`，IANA 标识）的本地墙钟为准。
- **解析**：每个命中日的本地名义时间被解析为一个确定的 UTC 时刻。
  解析只依赖规则、策略与时区数据，从不读取机器本地时区
  （`time.Local`），因此同一日程在不同机器、不同默认时区下算出的
  触发序列完全一致（有测试保证）。
- **间隙（gap）**：夏令时拨快时不存在的本地时间。检测到 0 个合法
  UTC 映射，按 `Policy.Gap` 处理：
  - `GapShiftForward`（默认）：顺延到间隙结束后的第一个有效本地时间
    （如美东 02:30 → 当天 03:00 EDT）；
  - `GapSkip`：本次显式跳过。跳过不是静默丢弃——该次触发以
    `Skipped=true` 出现在计算结果中并附带原因。
- **重复（overlap）**：夏令时拨回时出现两次的本地时间。检测到 2 个
  合法 UTC 映射，按 `Policy.Overlap` 取其一，**至多触发一次**：
  - `OverlapFirst`（默认）：取第一次出现（较早 UTC）；
  - `OverlapLast`：取第二次出现。
- 每次触发都携带 `Reason` 字段（如
  `gap: local time ... does not exist ..., shifted forward to ...`），
  取舍完全可解释、可审计。

## 配置项

```go
schedule.Policy{
    Gap:     schedule.GapShiftForward, // 或 GapSkip
    Overlap: schedule.OverlapFirst,    // 或 OverlapLast
}
```

引擎构造：`engine.New(store, tzDataVersion, deliverFunc)`
- `tzDataVersion`：当前时区数据版本，标记在每条新产生的投递记录上；
  运行期可通过 `SetTZDataVersion` 升级。**历史投递记录不可变**，
  规则升级不会重算或篡改已发生的触发。
- `deliverFunc`：提醒投递回调，返回错误则稍后自动重试。

## 语义保证

- **修改/取消**：`Update` 的生效边界是调用时刻；已按旧规则投递的
  历史记录（`EventFired`，绑定规则版本与时区数据版本）不被改写，
  之后的触发按新版本规则计算。`Cancel` 后不再产生新触发。
- **幂等投递**：每次触发的幂等键为 `scheduleID@utc`。重复调度、
  失败重试、并发 `DispatchDue` 都不会产生重复提醒（进程内 inflight
  登记 + 持久化 `EventFired` 去重）。进程重启后重放事件日志，
  宕机期间漏掉的触发会被补投、已投递的不会重复（不重不漏）。
  崩溃恰好发生在「投递成功、记录未落盘」之间时按 at-least-once
  处理，消费方可凭幂等键去重。
- **批量重排**：`Replan(now, horizon, ids)` 对一批日程重算窗口内的
  触发计划。重排期间到达的新增/修改直接写入事件日志并递增代际号，
  重排提交前校验代际、发现变化即基于最新状态重算，请求不丢失。
  返回的 `ReplanReport` 给出每个日程重排前后的触发对照与差异说明。
- **错误可区分**：`ErrNotFound` / `ErrUnauthorized` / `ErrInvalidTimezone`
  / `ErrInvalidRule` / `ErrInvalidTime` / `ErrCancelled`，
  均可用 `errors.Is` 判定。关键操作（create/update/cancel/dispatch/
  replan）写入审计日志。

## 测试

```sh
go test -race ./...
```

覆盖：DST 间隙顺延与显式跳过、DST 重复时刻只触发一次、跨机器/默认
时区一致性、月度规则缺日跳过、修改生效边界与历史不可变、取消、
并发与重试幂等投递、进程重启恢复、时区数据升级后历史稳定、
批量重排与并发请求不丢失。
