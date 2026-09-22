# 流管理设计草案

本文档用于收敛 `RTSP/ONVIF 拉流`、`RTMP/SRT 推流` 与 `lalmax/lal` 媒体状态之间的关系，解决“流已经进入媒体引擎，但在 NVR 中不可见、不可绑定、不可管理”的问题。

## 背景

当前项目已经把摄像头媒体链路收敛到 `lalmax`，但 `外部推流` 还没有完全建模为一等业务对象：

1. `lalmax embedded` 自带 `RTMP/RTSP/HTTP-FLV/WebRTC` 等媒体接入与分发能力
2. `internal/media.IngestHandler` 只负责把 lalmax publish 事件映射到 camera ID
3. API 与前端主要围绕 `camera_id` 建模
4. `lalmax` 中已有的 stream group 不能自然映射为“可见、可绑定、可操作”的业务对象

结果就是：

- 流可能已经进入 `lalmax`
- `mediaEngine.ListStreams()` 能看到它
- 但前端页面仍然看不到，或只能通过底层日志确认其存在

## 当前问题

### 1. 接入与业务绑定仍需分层

当前实际媒体接入已经只走 lalmax/lal：

- `lalmax/lal` 自带 RTMP/SRT 接入
- `internal/media.IngestHandler` 只保留 stream name 到 camera ID 的业务映射

后续仍需解决：

- 映射规则持久化
- Stream API 与前端可见性
- 推流生命周期的业务操作

### 2. 业务模型过度依赖 Camera

当前页面与接口大多默认：

- 一个可播放对象必须对应一个 `camera`
- 一个 live stream 必须通过 `/api/cameras/{id}/...` 访问

这适合物理设备，不适合外部推流。外部推流并不总是：

- 来自摄像头
- 具备 ONVIF / RTSP 配置
- 需要录像配置、保留策略、设备管理能力

### 3. 流存在但不可管理

当前系统缺少一个统一的“流视图”，导致：

- 无法列出所有活跃流
- 无法区分“已绑定摄像头的流”和“未管理的外部流”
- 无法对未绑定流执行提升、绑定、断开等操作

## 设计目标

目标不是只保留 pull，也不是继续维护双接入层，而是：

1. `lalmax/lal` 成为唯一媒体接入层
2. `lalmax-nvr` 成为唯一业务管理层
3. 同时支持：
   - pull：RTSP / ONVIF / HTTP-JPEG
   - push：RTMP / SRT
4. 引入统一的“流管理”模型
5. 将 `外部推流` 从 `Camera` 模型中解耦

## 核心原则

### 媒体接入统一

所有实际收流、转发、协议输出都尽量通过 `lalmax/lal` 完成：

- RTSP pull
- ONVIF 解析后 RTSP pull
- RTMP push
- SRT push

项目内部不再长期维护第二套正式 RTMP 主路径。

### 业务对象拆分

需要明确区分三个层次：

1. `Camera`
   - 物理设备
   - 拥有账号密码、ONVIF endpoint、拉流 transport、录像策略等属性

2. `Source`
   - 输入源定义
   - 可能来自 camera，也可能来自 RTMP/SRT/relay/manual

3. `LiveStream`
   - 当前运行中的媒体流实例
   - 来自 `lalmax` 运行时状态

## 推荐模型

### Camera

继续保留，适用于：

- RTSP 摄像头
- ONVIF 摄像头
- Xiaomi 摄像头

### Source

建议新增，最少字段：

- `id`
- `name`
- `type`: `camera | rtmp_ingest | srt_ingest | relay`
- `camera_id`：可空
- `ingest_key` / `stream_id` / `app_name`
- `enabled`

### LiveStream

建议作为运行态 API 对象，不一定落库：

- `stream_id`
- `app_name`
- `source_type`
- `source_ref`
- `managed`
- `bound_camera_id`
- `publisher_protocol`
- `video_codec`
- `audio_codec`
- `active`
- `last_seen`
- `publisher`
- `subscribers`

## 接口设计

### 第一阶段：只读可见性

新增：

- `GET /api/streams`
- `GET /api/streams/{stream_id}`

用途：

- 列出 `lalmax` 当前所有活跃流
- 先解决“推流进来了但系统里看不到”

返回建议包含：

- 流基本信息
- 发布者信息
- 当前协议状态
- 是否已映射到 camera

### 第二阶段：绑定与提升

新增：

- `POST /api/streams/{stream_id}/bind-camera`
- `POST /api/streams/{stream_id}/unbind-camera`
- `POST /api/streams/{stream_id}/promote`

语义：

- `bind-camera`：将外部流绑定到已有 camera
- `promote`：把未管理流 **登记为设备**（名称、位置、大屏）。**不会开始录像**；录像由 [录像计划](recording-plans.md) 决定

### 第三阶段：控制能力

新增：

- `DELETE /api/streams/{stream_id}`
- `POST /api/streams/{stream_id}/kick-publisher`

语义：

- 对 pull 源：停止 relay pull
- 对 push 源：断开发布者

## 前端设计

建议增加独立 `Streams` 页面，而不是继续塞到 `Cameras` 页面里。

页面分区建议：

1. `Managed Cameras`
   - 已绑定 camera 的流

2. `External Streams`
   - 已进入 `lalmax` 但未绑定 camera 的流

每条流可以提供：

- 预览入口
- publisher / subscriber 状态
- 绑定 camera
- 提升为 source
- 断开连接

## 对 media ingest 映射的处理建议

结论：

- **实际 ingest 保持在 lalmax/lal**
- **`internal/media.IngestHandler` 只作为业务胶水保留**

原因：

1. 它承担 `stream key -> camera_id` 和 `stream name -> camera_id` 的映射
2. 它避免在 lalmax-nvr 里复制 RTMP/SRT 协议代码
3. 后续可被持久化的 stream binding 模型替代

建议策略：

### 阶段 A

- 停止在 lalmax/lal 之外新增协议级 ingest 能力
- 所有新增 ingest 能力优先走 `lalmax`

### 阶段 B

- 完成 `GET /api/streams` 与 `Streams` 页面
- 让 `lalmax` 中已有流可见

### 阶段 C

- 完成绑定与控制能力
- 将 Settings 中的 `RTMP stream key -> camera ID` 改造为“绑定规则”

### 阶段 D

- 当 stream binding 完整建模后
- 下线临时的事件到 camera 映射胶水

## 不建议的方向

### 1. 只保留 pull，禁用 push

不建议。

原因：

- OBS / FFmpeg / 编码器天然更适合 push
- 某些来源无法被主动 pull
- 这会缩小接入能力，而不是简化模型

### 2. 把所有外部流都伪装成 Camera

不建议。

原因：

- Camera 是设备模型，不是流模型
- 会让外部推流混入设备管理、录像配置、ONVIF 能力判断等逻辑

## 分阶段实施计划

### Phase 1：流可见

目标：

- 先让所有活跃流在系统中可见

实施：

- 新增 `GET /api/streams`
- 新增 `Streams` 页面
- 页面展示来自 `mediaEngine.ListStreams()` 的流

### Phase 2：流归属

目标：

- 让流与 camera/source 的关系可见

实施：

- 增加绑定字段与归属识别
- 页面标注：
  - 已绑定 camera
  - 未管理外部流

### Phase 3：流操作

目标：

- 让外部流可绑定、可提升、可断开

实施：

- 增加 bind / promote / kick API
- 页面增加对应操作入口

### Phase 4：收口遗留接入

目标：

- 去掉第二套 RTMP 正式接入路径

实施：

- 停用 lalmax/lal 之外的协议级 ingest 代码
- 统一通过 `lalmax/lal` ingest + `lalmax-nvr` 业务绑定层工作

## 当前建议

建议按下面顺序推进：

1. 先做 `GET /api/streams`
2. 再做 `Streams` 页面
3. 再做"绑定 camera / 提升 source"
4. 最后用正式 stream binding 替代临时映射胶水

这是风险最小、收益最快的路径。

## 实现进度

### Phase 1：流可见 ✅ 已完成

- [x] `GET /api/streams` - 列出所有活跃流
- [x] `GET /api/streams/{stream_id}` - 获取单个流详情

### Phase 2：流归属 ✅ 已完成

- [x] `POST /api/streams/{stream_id}/bind-camera` - 绑定流到摄像头
- [x] `POST /api/streams/{stream_id}/unbind-camera` - 解绑流与摄像头
- [x] 数据库表 `stream_bindings` - 存储绑定关系

### Phase 3：流操作 ✅ 已完成

- [x] `POST /api/streams/{stream_id}/promote` - 把流登记为设备（不开始录像）
- [x] `DELETE /api/streams/{stream_id}` - 断开流（踢出发布者 + 停止拉流）
- [x] `POST /api/streams/{stream_id}/kick-publisher` - 踢出发布者

### API 接口列表

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/streams` | 列出所有活跃流 |
| GET | `/api/streams/{stream_id}` | 获取单个流详情 |
| POST | `/api/streams/{stream_id}/bind-camera` | 绑定流到摄像头 |
| POST | `/api/streams/{stream_id}/unbind-camera` | 解绑流与摄像头 |
| POST | `/api/streams/{stream_id}/promote` | 登记为设备（不开始录像） |
| DELETE | `/api/streams/{stream_id}` | 断开流 |
| POST | `/api/streams/{stream_id}/kick-publisher` | 踢出发布者 |

### 数据库变更

新增 `stream_bindings` 表：

```sql
CREATE TABLE stream_bindings (
    stream_id TEXT NOT NULL,
    camera_id TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (stream_id),
    FOREIGN KEY (camera_id) REFERENCES cameras(id)
);
```

### 测试覆盖

所有 API 端点都有完整的单元测试覆盖，包括：
- 成功场景
- 错误处理（流不存在、摄像头不存在等）
- 边界条件（无发布者、无媒体引擎等）

### 下一步工作

- [ ] 前端 `Streams` 页面开发
- [ ] 流状态实时更新（WebSocket/SSE）
- [ ] 流预览功能集成
- [ ] 批量操作支持
