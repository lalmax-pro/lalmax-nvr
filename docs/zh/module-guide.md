# 模块职责说明

本文描述 `internal/` 各包的职责、核心接口和扩展点。整体分层见 [架构概览](architecture.md)；包依赖关系以 `go.mod` 和实际 import 为准。

---

## 入口

### `cmd/lalmax-nvr`

程序入口。负责：
- CLI 参数解析（`main.go`）
- 应用组装：从配置文件创建各模块实例，注入依赖
- 生命周期管理：优雅启动 / 停止
- 集成测试也集中在此包（`*_test.go`）

---

## 核心业务层

### `internal/api`

HTTP 路由与请求处理。

- 使用 [chi](https://github.com/go-chi/chi) 路由框架
- 所有 handler 通过构造函数注入依赖，不使用全局变量
- 认证中间件：`RequireAuth` / `RequireOperatePermission`（superadmin）
- 路由注册集中在 `handler.go`，handler 文件按业务域分文件：
  - `handlers_cameras.go` — 摄像头 CRUD
  - `handlers_recording.go` — 录像计划与回放
  - `handlers_home_assistant.go` / `handlers_home_assistant_webrtc.go` — HA 目录与 WHEP
  - `handlers_transcoding.go` — 转码管理（Profile / Job / Variant）
  - `handlers_snapshot.go` — 快照抽帧（依赖 PATH 中的 FFmpeg）
  - ...

**扩展点**：新增 API 只需在 `handler.go` 注册路由，创建对应 handler 文件；认证、日志、CORS 等横切关注点在 `middleware/` 处理。

---

### `internal/camera`

摄像头生命周期管理。

- 持有每路摄像头的运行状态（连接中 / 在线 / 离线）
- 负责拉流启动：调用 `internal/media` 建立 RTSP / ONVIF 拉流
- 子码流管理
- IP 地址自愈（摄像头重启后 IP 变更的自动探测与修复）
- 组写入器覆盖：已由 `recorder.GroupWriter` 订阅的摄像头，不再另开一路录像器

**注意**：MJPEG / HTTP JPEG 仍由本包直接拉帧（lalmax 不处理这类源）。

---

### `internal/media`

lalmax 媒体引擎的 Go 适配层。

- 封装 lalmax / lal 的 pull 接口、RTP 收口、GetStream、BuildPlayURL 和流事件
- `mode: embedded`：引擎与 NVR 同进程，通过 lalmax Go API 调用
- `mode: http`：连外部 lalmax HTTP 接口（适合独立部署场景）
- 提供 `IngestHandler`：将 lalmax publish 事件映射到 NVR 的 stream_id / camera_id 业务身份

**关键接口**（`types.go`）：
```go
type Engine interface {
    Pull(ctx, source SourceSpec) error
    GetStream(streamID string) (StreamInfo, error)
    BuildPlayURL(streamID, protocol string) (string, error)
    Subscribe(handler EventHandler)
    // ...
}
```

---

### `internal/recorder`

录像管理，分两种模式：

1. **组写入器（GroupWriter）**：直接订阅 lalmax group，获取原始 H.264/H.265 帧，按段写入磁盘 MP4。这是 embedded 模式下的主要录像路径。
2. **录像任务（record task）**：用于 `adaptive` 策略和 `media.mode: http` 场景，走 lalmax HTTP 协议录像。

录像计划（Plan）只控制写盘开关（`SetWriting(true/false)`），不决定如何写盘。

---

### `internal/merge`

片段合并，负责：

- **滚动合并**：片段一关（segment.completed 事件）就 debounce（默认 5s）合进当前 UTC 小时文件
- **周期合并**：定时扫描历史补齐，处理录像中途重启遗留的零散片段
- 输出为"小时桶"MP4，VOD 回放以此为单位切 fMP4

---

### `internal/vod`

点播服务，按需切分已录制内容。

- 接受播放请求，从小时桶 MP4 中切出 init segment + fMP4 分片（约 6 秒/片）
- 生成 HLS VOD playlist（`#EXT-X-DISCONTINUITY` 处理录像缺口）
- 不依赖外部转码工具，直接操作 MP4 box

---

### `internal/health`

多层健康探活与自动修复。

- 定期探测每路摄像头的连通性
- 触发 `internal/camera` 的自愈逻辑（重连、IP 刷新）
- 健康状态聚合暴露给 `/api/health/details` 和 `/api/health/cameras`

---

## 媒体接入层

### `internal/gb28181`

GB28181 SIP 上级平台实现。

- SIP 服务：设备 REGISTER、Catalog 查询、INVITE 触发设备推流
- 设备目录管理：`gb28181_devices` + `gb28181_channels`
- 级联：通过 `PlatformManager` 向上级平台注册并共享通道（`gb28181_platforms` + `gb28181_platform_channels`）
- 媒体：设备推送 PS/RTP，解包后注入 lalmax group

**设计草案**：GB28181 标准目录树（行政区划 + 业务分组）见 [GB28181 目录设计](gb-catalog-design.md)。

---

### `internal/voip`

独立 SIP 终端 / PBX 功能。

- 与 GB28181 完全独立，服务不同场景（内线电话、可视门禁、PBX 分机）
- 功能：SIP 终端注册、呼入 / 外呼、DTMF、通话记录
- 网页对讲通过 WebRTC 实现（浏览器麦克风 ↔ SIP 媒体桥接）
- 通话媒体发布为 lalmax group，可选择录像

---

### `internal/onvif`

ONVIF 设备发现与控制。

- WS-Discovery / Hello 组播发现
- `GetStreamUri` 获取 RTSP 地址，交给 `camera` 层拉流
- PTZ 控制（ContinuousMove / AbsoluteMove / Stop）

---

### `internal/autodiscover`

网络内设备自动发现，与 ONVIF 配合完成局域网摄像头扫描。

---

## 转码层（`internal/transcode`）

通用实时转码能力，详细设计见 [转码架构设计](transcoding-design.md)。

**核心对象**：

| 文件 | 内容 |
|------|------|
| `types.go` | `SourceRef`、`Profile`、`Job`、`State`，参数校验和共享键（`WorkKey`）|
| `manager.go` | `Manager`：任务调度、共享去重、配额控制 |
| `service.go` | `Service`：协调器主循环，状态机驱动 |
| `executor.go` | `Executor`：解析媒体地址、启动后端、健康检查 |
| `ffmpeg.go` | FFmpeg 后端实现 |
| `gstreamer.go` | GStreamer 后端实现（固定管线） |
| `capabilities.go` | 能力检测与自检缓存 |
| `repository.go` | `transcode_intent_v1` / `transcode_outputs_v1` 数据库操作 |
| `variants.go` | 逻辑输出（Variant）管理与播放地址解析 |

**扩展点**：新增后端实现 `Backend` 接口（`executor.go`），注册到 `NewExecutor` 工厂。

---

## 存储层

### `internal/storage`

数据库抽象层，支持 SQLite / PostgreSQL / MySQL。

- 使用 `database/sql` 标准库，不引入 ORM
- 迁移版本管理：`db.go` 中按版本顺序执行 DDL
- **注意**：转码表（`transcode_intent_v1` / `transcode_outputs_v1`）使用 `CREATE TABLE IF NOT EXISTS` 初始化，不复用历史跳过的转码迁移版本

---

### `internal/model`

共享数据模型，定义各层通用的 struct（Camera、Recording、Event 等）。

---

## 基础设施层

### `internal/auth`

认证与鉴权。

- NVR HTTP 账号管理（用户 / 密码哈希）
- 播放 token 管理（`auth.playback_tokens`）
- RTSP 鉴权配置（`media.rtsp_auth_enable`）

### `internal/event`

事件总线，解耦各模块之间的状态通知。

- 录像完成 → merge 触发
- 健康状态变化 → camera 修复
- AI 检测事件 → MQTT 推送

### `internal/middleware`

HTTP 中间件：日志、CORS、鉴权、请求 ID、限流等。

### `internal/observability`

OpenTelemetry 集成：Trace / Metric / Log 导出到 OTLP。

---

## 其他功能包

| 包 | 职责 |
|----|------|
| `internal/ai` | AI 目标检测（YOLO）子进程管理，`CGO_ENABLED=0` |
| `internal/dlna` | DLNA / SSDP 服务发现与媒体服务 |
| `internal/emby` | Emby 影音库拉取与发布为直播流 |
| `internal/iptv` | M3U 订阅源管理、频道检测与 HLS 代理 |
| `internal/mqtt` | MQTT 事件推送 |
| `internal/ftp` | FTP 服务（`fclairamb/ftpserverlib`） |
| `internal/webdav` | WebDAV 文件浏览 |
| `internal/jt808` | JT/T 808 信令：注册、直播、回放指令下发 |
| `internal/ban` | IP 封禁管理 |
| `internal/cleanup` | 录像文件过期清理 |
| `internal/rediscovery` | 摄像头 IP 变更重新发现 |
| `internal/streamhistory` | 历史流记录查询 |
| `internal/civilcode` | GB/T 2260 行政区划代码工具 |
| `internal/linkage` | 联动规则（事件 → 录像 / 推流等） |
| `internal/metrics` | Prometheus 指标导出 |
| `internal/docsportal` | 嵌入 OpenAPI 文档的 HTTP 服务 |
| `internal/ui` | Svelte 前端静态文件嵌入（`go:embed`） |

---

## 第三方包

| 路径 | 说明 |
|------|------|
| `third/lalmax` | 内嵌 lalmax / lal 媒体引擎（Go 模块替换） |
| `onvif/` | ONVIF 协议实现辅助库 |

---

## 常见扩展场景

**新增媒体接入协议**
1. 在 `internal/media` 中扩展 `Engine` 接口或添加新的 ingest handler
2. 在 `internal/camera` 中添加对应的启动逻辑
3. 更新 `architecture.md` 的接入路径表格

**新增 API 端点**
1. 在 `internal/api/handler.go` 注册路由
2. 新建 `handlers_<domain>.go` 文件
3. 必要时在 `internal/docsportal/openapi.yaml` 补充 OpenAPI 定义

**新增转码后端**
1. 实现 `internal/transcode.Backend` 接口
2. 在 `executor.go` 的工厂函数中注册
3. 在 `capabilities.go` 中添加对应的自检逻辑
