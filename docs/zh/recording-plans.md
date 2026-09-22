# 录像计划

录像由 **计划** 驱动，计划挂在 **lalmax 流**（`stream_id`）上，不挂在摄像头上。

没有计划的流 **不会录制**。把流登记为设备（原先叫「提升」）也 **不会自动开录**。

Web UI：侧栏 **录像计划**（`#/recording-plans`）。REST：`/api/recording-plans`。

## 流、设备、计划

| | 流 | 设备（Camera） | 录像计划 |
|---|---|---|---|
| 是什么 | lalmax group `live/{stream_id}` | 名称、位置、PTZ、大屏、分组 | 何时写盘 |
| 怎么来 | RTSP/ONVIF 拉流、GB28181、RTMP/SRT/WHIP 推流 | 添加摄像头，或把已有流 **登记为设备** | 在录像计划页创建 |
| 录像 | 有生效计划才录 | 不决定是否录像 | 唯一策略源 |

典型用法：

- **只想录一条推流**：建计划即可，不必登记设备。
- **要管一台摄像头**：添加设备或登记流；要录像再给它的 `stream_id` 建计划。
- **登记为设备**：给流一个运维身份（名字、地图、大屏）。API 仍是 `POST /api/streams/{stream_id}/promote`。

当前缺口：录像回放页仍按 **摄像头列表** 筛选。纯流录像会写入 `recordings.stream_id`（无设备时 `camera_id` 也是流 ID），但 UI 里选不到设备就看不见。

## 模式

| `mode` | 行为 |
|--------|------|
| `continuous` | 24/7 录像 |
| `scheduled` | 仅在周计划窗口内录（`windows`） |
| `event` | MQTT / ONVIF 移动等事件窗口内录 |
| `adaptive` | 平静时稀疏关键帧，活动时全速；间隔用设备上的 `adaptive.timelapse_interval` |
| `off` | 计划存在但不写盘（预览仍可） |

`enabled: false` 与 `mode: off` 都会让调度器停写。一条流最多一份计划。

开录、写盘、停录的流程图见 [录制流程](recording-flow.md)。

## 调度

`RecordingPlanner` 从 `recording_plans` 载入期望状态。`RecordingScheduler` 对账「计划此刻要录」和「lalmax 里还在的流」：

- 两边都满足：`TaskManager.Ensure`
- 计划关掉、事件窗口结束，或流不在 lalmax：`TaskManager.Stop`
- 有没有登记成设备不影响这条判断

设备离线、删除设备、流下线会立刻停掉该流的 task。服务关闭时 `StopAll`。流重新进入 lalmax 且计划仍要录时再 `Ensure`。

embedded 模式下帧来自进程内订阅；http 模式回退到 lalmax RTSP。MJPEG、HTTP JPEG、小米和延时摄影仍由设备侧录像器写盘。

## API

```
GET    /api/recording-plans
POST   /api/recording-plans
GET    /api/recording-plans/{id}
PUT    /api/recording-plans/{id}
DELETE /api/recording-plans/{id}
```

创建示例：

```bash
curl -u admin:password -X POST http://localhost:9090/api/recording-plans \
  -H 'Content-Type: application/json' \
  -d '{"stream_id":"obs-1","name":"OBS","mode":"continuous","enabled":true}'
```

定时窗口：`mode` 为 `scheduled`，`windows` 为 `{day_of_week:0-6, start_time:"HH:MM", end_time:"HH:MM"}`。

摄像头 JSON 里的 `recording_mode` 是只读派生：取绑定流的计划；没有计划则为 `off`。`POST /api/cameras/:id/pause-recording` 会把该流计划设为 `enabled: false`。

录像行带 `stream_id`。有设备时 `camera_id` 是设备 ID；纯流录像时两者都是流 ID。
