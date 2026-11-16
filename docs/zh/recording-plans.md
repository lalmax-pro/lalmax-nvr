# NVR-03 录像与回放规范

## 1. 基本原则

**REC-001｜无计划不录像。** 常规录像由绑定 `stream_id` 的 RecordingPlan 驱动。添加设备、启动拉流、收到推流、把流登记为设备（`promote`）均不会自动创建计划。

**REC-002｜流与设备分离。** 一条流可以没有设备而直接录像；一个设备可以在线但不录像。设备 JSON 中的 `recording_mode` 是由绑定流计划派生的只读字段，无计划时为 `off`。

**REC-003｜一条流最多一份计划。** `recording_plans.stream_id` 唯一；对已有计划的流再次 `POST` 返回 `409`，修改应使用该计划的 `PUT /api/recording-plans/{id}`。

**REC-004｜直播与写盘解耦。** 暂停、关闭计划或离开计划时间窗不应被理解为关闭直播；设备停止、流下线才会终止相应媒体会话。

Web 入口为 **录像计划**（`#/recording-plans`），接口为 `/api/recording-plans`。

## 2. 计划数据契约

```json
{
  "id": "plan-...",
  "stream_id": "front-door",
  "name": "Front door",
  "mode": "scheduled",
  "enabled": true,
  "windows": [
    { "day_of_week": 1, "start_time": "08:00", "end_time": "18:00" }
  ],
  "created_at": "2026-01-01T00:00:00Z",
  "updated_at": "2026-01-01T00:00:00Z"
}
```

| 字段 | 创建规则 | 说明 |
|------|----------|------|
| `stream_id` | 必填 | 关联流，不是显示名称 |
| `name` | 可省略 | 空时存储层使用 `stream_id` |
| `mode` | 可省略 | 空时默认 `continuous` |
| `enabled` | 可省略 | 创建时默认 true；禁用后不写盘 |
| `windows` | `scheduled` 时使用 | 周计划窗口数组 |
| `id` / 时间 | 服务端生成 | 创建请求不依赖客户端提供 |

更新接口支持保留省略字段：省略 `stream_id` 时保留原绑定，省略 `mode`、`name`、`enabled`、`windows` 时保留对应旧值。集成方不应把 `PUT` 假定成所有服务端资源都通用全量替换；以各资源的 OpenAPI schema 为准。

## 3. 模式与期望状态

| `mode` | `enabled=true` 时的常规期望状态 | 事件影响 |
|--------|--------------------------------|----------|
| `continuous` | 持续写盘 | 不改变基本语义 |
| `scheduled` | 仅在当前命中的周窗口内写盘 | 事件窗口也可参与调度 |
| `event` | 常规期望为不写盘 | 事件窗口活动时写盘 |
| `adaptive` | 使用自适应录像路径 | 平静时稀疏关键帧，活动时全速 |
| `off` | 不写盘 | 不应把 `off` 当作事件模式使用 |

`enabled=false` 无论模式均表示常规不写盘。事件窗口由事件管理器单独计算，因此 `event` 计划在数据库期望状态中显示 false 并不代表事件期间永远不会写。

**REC-101｜实际写盘判定。** 调度输入至少包含计划期望、事件活动状态和当前存活流；设备是否已登记不是判定条件。

## 4. 周计划窗口

| 字段 | 规则 |
|------|------|
| `day_of_week` | `0=Sunday` 至 `6=Saturday` |
| `start_time` / `end_time` | `HH:MM`；客户端必须提供有效的 24 小时时间 |
| 普通窗口 | 开始时间包含，结束时间不包含，即 `[start,end)` |
| 跨夜窗口 | `start > end` 时，从当天 start 到次日 end |
| 零长度窗口 | `start == end` 不命中 |
| 时区 | 使用 NVR 进程本地时间判断；数据库时间戳规范与计划本地时间不是同一概念 |

示例：周一 `22:00` 到周二 `06:00` 使用 `{day_of_week:1,start_time:"22:00",end_time:"06:00"}`，周二凌晨由前一日窗口命中。

## 5. 调度与执行路径

**REC-201｜计划变更及时协调。** 计划 API 修改后刷新 Planner 并请求立即对账；后台调度器还会每 30 秒周期对账。编解码尚未就绪等情况使用重试，不应假定一次 `Ensure` 就代表持续录像成功。

**REC-202｜存活流约束。** 只有流仍在媒体引擎中且计划/事件需要时才启动或保持相应录像执行路径。流消失后 record task 停止；流恢复且计划仍需要时重新建立。

| 执行路径 | 适用范围 | 计划如何作用 |
|----------|----------|--------------|
| GroupWriter 组写入器 | embedded 模式下被覆盖的 H.264/H.265 等流 | 保持组订阅，通过 `SetWriting` 控制写盘 |
| TaskManager / record task | `adaptive`、HTTP 模式及其他未被组写入器覆盖的流 | 按需 `Ensure` / `Stop` |
| 专用采集器 | MJPEG、HTTP JPEG、延时摄影等 | 按设备与计划状态控制专用录像器 |
| VoIP 自动通话录像 | 按 SIP 用户的 `record_calls` 配置 | 属于独立业务触发，不等同于手工流计划 |

**REC-203｜不得重复录制。** 同一路流只能由适用的录像路径负责；组写入器与 record task 的跳过/写开关逻辑用于避免双写。

详细内部时序、缓存 GOP、时间戳和模块接口见[录制流程](recording-flow.md)。

## 6. 事件与自适应录像

事件窗口全局配置：

```yaml
event:
  post_roll: 30s
  max_duration: 5m
```

- `post_roll`：最后一次触发后继续录像的时长，默认 30 秒。
- `max_duration`：单个事件会话的最大时长，默认 5 分钟。
- MQTT、ONVIF 移动侦测和告警规则可形成活动来源，具体格式见[MQTT](mqtt-integration.md)、[ONVIF](onvif-guide.md)和[事件中心](events.md)。

自适应平静期关键帧间隔由设备的 `adaptive.timelapse_interval` 控制，默认 30 秒；代码将有效间隔限制在 5 秒至 10 分钟，非法或越界配置回退默认值。

## 7. 片段与文件生命周期

**REC-301｜关键帧起段。** 新 MP4 片段必须等到可用参数集和关键帧，避免从无法解码的帧开始。达到目标片段时长后，在后续关键帧边界切分，因此 `storage.segment_duration` 是目标而非严格实时时长。

**REC-302｜临时对象不视为可回放录像。** 写入使用 `.tmp` 文件或目录；封装关闭后同步并原子重命名为最终对象，再登记录像索引。进程启动会清理遗留临时对象并修复可识别的孤立文件。

**REC-303｜目录按来源编码。** 文件位于 `{storage.root_dir}/{encoded-source-id}/`，不是固定的 `recordings/{encoding}/{camera_id}/`。特殊字符和 Windows 保留名使用可逆编码；客户端不得自行猜测物理路径。

**REC-304｜索引和文件均需一致。** 正常清理先删除文件再删除数据库索引；文件删除失败时保留索引以便重试。录像列表中的记录不应被当作文件完整性的永久保证，运维验收应抽查实际播放。

## 8. 合并

```yaml
merge:
  enabled: true
  check_interval: 1h
  window_size: 1h
  batch_limit: 200
  min_segment_age: 10m
  min_segments_to_merge: 3
  rolling_enabled: true
  rolling_debounce: 5s
```

- 周期合并处理达到最小年龄、满足窗口和最小段数的历史候选。
- 滚动合并在 `merge.enabled=true` 且 `rolling_enabled` 未显式关闭时生效；片段关闭后经过 debounce，并入当前 UTC 时间桶。
- 合并对象是已关闭片段；播放和清理应使用合并后的索引状态，而不是继续假定每个原始片段都独立存在。
- 设备级合并覆盖只覆盖非零字段；普通 `enabled` 布尔值只能把全局 false 覆盖为 true，不能用该结构把已启用的全局合并单独覆盖为 false。

## 9. 保留、锁定与删除

| 机制 | 规则 |
|------|------|
| 时间保留 | 对设备录像按设备 `retention_days`；0/未设置回退全局 `cleanup.retention_days`，不是永久保留 |
| 磁盘阈值 | 使用率超过 `cleanup.disk_threshold_percent` 时，从最早的可删除录像开始清理，直到回到阈值或无可删除候选 |
| 锁定 | `locked=true` 的录像不参与常规保留/磁盘清理，删除接口返回冲突；必须先解锁 |
| 归档 | 归档保留期单独处理；归档的 `archive_retention_days=0` 表示永久保留，与活动设备的 0 语义不同 |
| 纯流来源 | 无设备的纯流录像不能依赖“逐设备保留天数”路径；必须同时配置并监测磁盘阈值，必要时制定额外清理策略 |

**REC-401｜清理不是容量保证。** 锁定文件、文件权限错误、非录像占用和删除失败均可能使磁盘继续增长。必须配合磁盘告警和人工处置，不得只依赖保留天数。

## 10. 回放、导出与来源

| 接口 | 用途 |
|------|------|
| `GET /api/recordings` | 分页查询录像，支持来源、时间、格式、合并和归档筛选 |
| `GET /api/recordings/timeline` | 获取指定来源和时间范围的轻量时间轴 |
| `GET /api/recordings/sources?include_archived=true` | 包含历史来源的录像入口 |
| `GET /api/recordings/{id}` | 单条录像元数据 |
| `GET /api/recordings/{id}/download` | 下载或预览，支持 HTTP Range |
| `POST /api/recordings/{id}/lock` / `unlock` | 锁定/解锁 |
| `GET /api/cameras/{id}/playback/playlist.m3u8` | 按天连续 VOD HLS fMP4 |
| `GET /api/cameras/{id}/playback/export.m3u8` | 按时间范围导出播放列表 |

**REC-501｜连续回放允许缺口。** VOD 按已落盘样本组织，录像不连续时使用 discontinuity 等机制分隔；缺口不是必须由虚拟视频填充的内容。MJPEG 等专用格式仍走其适用的文件/帧回放路径。

**REC-502｜历史来源独立存在。** 删除流记录或设备不必然删除历史录像。录像页应通过来源列表和时间轴查询已有记录，不应只遍历当前在线设备。

## 11. 验收清单

1. 无计划时，目标流可直播但没有新增常规录像。
2. 创建连续计划且流上线后，能产生已关闭、可下载、可回放的片段。
3. 禁用计划或将模式设为 `off` 后停止新增写盘，直播仍可用。
4. 定时计划覆盖普通窗口、跨夜窗口、窗口边界和服务器时区。
5. 事件计划只在事件窗口及 post-roll 内写盘，且不超过最大事件时长。
6. 断流时停止任务或收尾当前段；恢复后按计划重新写盘，时间轴能表达缺口。
7. 锁定录像不被自动清理或普通删除；解锁后恢复策略。
8. 磁盘阈值、保留期和归档规则分别验证，不以单一测试代替全部路径。
