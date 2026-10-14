# lalmax-nvr 配置参考文档

lalmax-nvr 使用 YAML 配置文件控制各模块。分层、端口与数据流见 [架构](architecture.md)；安装见 [快速入门](getting-started.md)。

示例文件：仓库内 [`config/config.example.yaml`](../../config/config.example.yaml)。二进制默认读取 `config/lalmax-nvr.yaml`。

## 配置文件结构

以下只展示部分字段；新部署请以[配置示例](../../config/config.example.yaml)为起点。摄像头通常通过 Web UI 添加并保存在数据库中。

```yaml
server:
  listen: ":9090"
storage:
  root_dir: "/var/lib/lalmax-nvr"
  segment_duration: "30s"
media:
  mode: "embedded"
auth:
  username: "admin"
  password_hash: ""
  password: ""
cameras:
  - id: "cam1"
    name: "摄像头名称"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://..."
    enabled: true
    onvif_endpoint: ""           # ONVIF 特定
    profile_token: ""            # ONVIF 特定
    stream_encoding: ""          # ONVIF 自动检测 (H264/H265)
    sub_stream_url: "rtsp://..."  # 实时预览子流
    snapshot_url: "http://..."    # JPEG 快照缩略图
    sample_interval: 1            # MJPEG 帧采样间隔
    hls_max_fps: 0               # HLS 帧率限制
    vendor: ""                   # 小米传输供应商
    merge:                       # 每个摄像头合并配置覆盖
      enabled: false
      check_interval: "1h"
      window_size: "1h"
      batch_limit: 150
      min_segment_age: "5m"
      min_segments_to_merge: 2
cleanup:
  retention_days: 30
  check_interval: "1h"
  disk_threshold_percent: 95
merge:
  enabled: false
  check_interval: "1h"
  window_size: "1h"
  batch_limit: 200
  min_segment_age: "10m"
  min_segments_to_merge: 3
  rolling_enabled: true
  rolling_debounce: "5s"
ftp:
  enabled: true
  port: 2121
  passive_port_range: "2122-2140"
mqtt:
  enabled: false
  broker: "tcp://localhost:1883"
  topic: "lalmax-nvr"
  client_id: "lalmax-nvr"
  username: ""
  password: ""
webdav:
  enabled: true
  path_prefix: "/dav"
  read_write: false
dlna:
  enabled: false
  port: 8200
iptv:
  max_concurrent_pulls: 32
hls:
  enabled: false                 # 是否启用 HLS/LL-HLS 播放（默认关闭）
  on_demand: true                # 按需切片：仅在有观众访问时切片（默认开启）
  idle_timeout: "60s"            # 无 HLS 访问后停止切片的超时（默认 60s）
  write_buffer_size: 100         # 每个流的异步帧缓冲区
  segment_max_size_mb: 10        # HLS 片段最大大小 (MB)
  segment_count: 7               # 每个流的片段数 (范围: 3-10)
  max_streams: 4                 # 最大并发流数 (范围: 1-20，RPi 限制: 4)
xiaomi:
  user_id: ""                    # 小米账户用户 ID (来自认证响应)
  token: ""                      #小米 passToken API 访问令牌
  region: "cn"                   # 区域代码: cn, sg, de 等
observability:
  log_level: "info"              # 日志级别: debug, info, warn, error
  log_format: "text"             # 日志格式: json 或 text
  enable_pprof: false            # 启用 pprof 调试端点
  trace_retention: "24h"         # 本地调用链保留时长
  max_local_traces: 10000        # 本地最多保留的调用链条数
  local_trace_enabled: true      # 是否保存本地调用链；也可在 Web 设置中切换
  trace_ui_url_template: ""      # 第三方 Jaeger/Tempo 地址模板，需包含 {trace_id}
  otlp:
    enabled: false               # 是否导出 traces、metrics 和 logs 到 OTLP/HTTP Collector
    endpoint: "http://127.0.0.1:4318"
    service_name: "lalmax-nvr"
    traces_enabled: true
    metrics_enabled: true
    logs_enabled: true
    timeout: "10s"
    metrics_export_interval: "30s"
    logs_export_interval: "1s"
version: "1.0"
```

## 服务器配置

### `server.listen`
- **类型**: string
- **默认**: `":9090"`
- **描述**: Web 服务器监听地址和端口
- **示例**: `":8080"` 或 `"192.168.1.100:9090"`

## 存储配置

### `storage.root_dir`
- **类型**: string
- **默认**: `/var/lib/lalmax-nvr`（二进制）或 `/data`（Docker）
- **描述**: 存储录像、数据库和临时文件的根目录。所有摄像头录像存储在 `{root_dir}/{camera_id}/` 下。
- **Docker**: 在 Docker 中运行时，通过 `NVR_DATA_DIR` 环境变量设置。卷挂载和 `NVR_DATA_DIR` 必须一致。
- **二进制**: 可通过 `lalmax-nvr init --data-dir` 参数设置，或直接在 YAML 配置中指定。
- **示例**: `/var/lib/lalmax-nvr`、`/mnt/external/nvr`、`/data`

### `storage.segment_duration`
- **类型**: string
- **默认**: `"30s"`
- **描述**: 视频片段时长（内存密集型）
- **重要**: 每个片段在完成前会将所有视频数据保存在 RAM 中
- **内存使用**:
  - 30s 片段: ~15-20MB 每片段
  - 60s 片段: ~30-40MB 每片段
  - 120s 片段: ~60-80MB 每片段
- **RPi 限制**: 树莓派 3B 上最大 30 秒
- **示例**: `"30s"`, `"1m"`, `"5m"`

## 身份验证配置

### `auth.username`
- **类型**: string
- **必需**: 是（Web UI 和 FTP 需要）
- **描述**: 身份验证用户名
- **示例**: `"admin"`

### `auth.password_hash`
- **类型**: string
- **必需**: 是（Web UI 和 FTP 需要）
- **描述**: bcrypt 哈希密码。使用 `lalmax-nvr hash-password <password>` 生成
- **优先级**: 如果同时设置了 `password` 和 `password_hash`，`password_hash` 优先
- **注意**: 如果只提供了 `auth.password`（明文），服务器会在首次启动时自动生成哈希值并写回到配置文件的 `password_hash`，然后清除 `password` 字段
- **示例**: `$2a$10$N9qo8uLOickgx2ZMRZoMy...`

### `auth.password`
- **类型**: string
- **可选**: 是
- **描述**: 明文密码，方便初始设置。首次运行时，服务器会自动哈希此值并写入到 `password_hash`，然后清除 `password` 字段
- **优先级**: 仅在 `password_hash` 为空时使用
- **示例**: `"admin123"`

## 摄像头配置

### 摄像头结构
每个摄像头配置需要这些基本字段：

```yaml
cameras:
  - id: "cam1"
    name: "摄像头名称"
    protocol: "rtsp"
    encoding: "h264"
    url: "摄像头地址"
    enabled: true
```

### `cameras[].id`
- **类型**: string
- **必需**: 是
- **描述**: 摄像头的唯一标识符（如果未提供则自动生成）
- **格式**: 字母数字，推荐用 kebab-case（例如 "front-door"）
- **示例**: `"front-door"`, `"cam-01"`

### `cameras[].name`
- **类型**: string
- **必需**: 是
- **描述**: 人类可读的摄像头名称
- **示例**: `"前门摄像头"`, `"后院"`

### `cameras[].protocol`
- **类型**: string
- **必需**: 是
- **描述**: 摄像头传输协议
- **选项**: `"rtsp"`, `"http"`, `"onvif"`, `"xiaomi"`
- **旧格式**: 也支持 `"rtsp_h264"`, `"rtsp_h265"`, `"rtsp_mjpeg"`, `"http_jpeg"`（自动解析为新格式）
- **注意**: 旧格式会自动解析为新协议+编码格式以保持向后兼容性
- **兼容性**: 两种格式都支持

### `cameras[].encoding`
- **类型**: string
- **可选**: 是（从旧协议自动检测或根据协议设置默认值）
- **描述**: 视频编码格式
- **选项**: `"h264"`, `"h265"`, `"mjpeg"`, `"jpeg"`
- **有效组合**:
  - `protocol: "rtsp"` → `encoding: "h264"`, `"h265"`, 或 `"mjpeg"`
  - `protocol: "http"` → `encoding: "jpeg"`
  - `protocol: "onvif"` → `encoding: "h264"` 或 `"h265"`（如果未指定则自动检测）
  - `protocol: "xiaomi"` → `encoding: "h264"` 或 `"h265"`（自动检测）

### `cameras[].url`
- **类型**: string
- **必需**: 是（ONVIF 和 Xiaomi 摄像头除外）
- **描述**: 摄像头地址或流端点
- **示例**:
  - RTSP: `"rtsp://192.168.1.100:554/stream"`
  - HTTP: `"http://192.168.1.101/capture"`
  - ONVIF: `"http://192.168.1.102:80/onvif/device_service"`（或使用 `onvif_endpoint`）
- **验证**: 必须有有效的协议（http/rtsp）和主机

### `cameras[].username`
- **类型**: string
- **可选**: 是
- **描述**: 摄像头身份验证用户名
- **示例**: `"admin"`

### `cameras[].password`
- **类型**: string
- **可选**: 是
- **描述**: 摄像头身份验证密码
- **示例**: `"摄像头密码"`

### `cameras[].enabled`
- **类型**: boolean
- **默认**: `true`
- **描述**: 是否启用摄像头录制
- **示例**: `true` 或 `false`

### `cameras[].onvif_endpoint`
- **类型**: string
- **可选**: 是（ONVIF 摄像头如果未提供 URL 则必需）
- **描述**: ONVIF 设备服务端点地址
- **示例**: `"http://192.168.1.100:80/onvif/device_service"`
- **注意**: 如果为 ONVIF 摄像头设置了 URL，会自动复制到 onvif_endpoint

### `cameras[].profile_token`
- **类型**: string
- **可选**: 是
- **描述**: ONVIF 媒体配置文件令牌，用于特定流选择
- **示例**: `"profile_1"`
- **注意**: 可选，如果未指定则使用默认配置文件

### `cameras[].stream_encoding`
- **类型**: string
- **可选**: 是
- **描述**: ONVIF 摄像头的流编码 (H264 或 H265)
- **选项**: `"H264"`, `"H265"`
- **注意**: 空 = 从 ONVIF 设备功能自动检测

### `cameras[].sub_stream_url`
- **类型**: string
- **可选**: 是
- **描述**: 实时 HLS 预览的低分辨率 RTSP 子流地址。配置后，仪表板将使用此流而不是主流，减少带宽使用
- **注意**: 子流必须使用与主流相同的编解码器（H.264/H.265）
- **示例**: `"rtsp://192.168.1.100:554/stream2"`

### `cameras[].snapshot_url`
- **类型**: string
- **可选**: 是
- **描述**: 返回 JPEG 快照图像的 HTTP 地址。配置后，仪表板将显示快照缩略图而不是实时 HLS 流，显著减少带宽
- **行为**: 快照缓存 10 秒；摄像头暂时无法访问时提供缓存的内容
- **示例**: `"http://192.168.1.100/snapshot"`, `"http://192.168.1.100/cgi-bin/snapshot.cgi"`

### `cameras[].sample_interval`
- **类型**: integer
- **可选**: 是
- **默认**: 1（仅限 MJPEG 摄像头）
- **描述**: MJPEG 帧采样间隔（秒）。较高的值可减少 CPU 使用率但降低帧率
- **示例**: `1`, `2`, `5`

### `cameras[].hls_max_fps`
- **类型**: integer
- **可选**: 是
- **默认**: 0（无限制）
- **描述**: HLS 流媒体的最大帧率。0 = 无限制
- **示例**: `30`, `15`, `25`

### `cameras[].vendor`
- **类型**: string
- **可选**: 是
- **描述**: 小米摄像头的传输供应商
- **选项**: `"cs2"`（默认）
- **示例**: `"cs2"`

### `cameras[].audio_enabled`

- **类型**: boolean
- **默认行为**: 从 YAML 载入时，H.264/H.265 的 RTSP、ONVIF、小米摄像头会自动开启；JPEG/MJPEG 不支持音频。通过 API 添加设备时，可显式传 `false` 关闭。
- **描述**: 控制是否把源流中的受支持音频写入录像；源流本身必须有可用音频轨。
- **示例**: `true`、`false`

### `cameras[].did`

- **类型**: string
- **可选**: 是（小米摄像头必需）
- **描述**: 来自云服务的小米设备 ID
- **示例**: `"xiaomi_device_id_123"`

### `cameras[].merge`
- **类型**: object
- **可选**: 是
- **描述**: 每个摄像头合并配置覆盖
- **注意**: 只有非零字段会覆盖全局合并配置
- **示例**: 参见 [合并配置](#合并配置)

## 清理配置

### `cleanup.retention_days`
- **类型**: integer
- **默认**: 30
- **范围**: 1-3650
- **描述**: 删除超过 N 天的录像
- **示例**: `7`, `30`, `90`

### `cleanup.check_interval`
- **类型**: string
- **默认**: `"1h"`
- **描述**: 检查过期录像的频率
- **示例**: `"30m"`, `"1h"`, `"2h"`

### `cleanup.disk_threshold_percent`
- **类型**: integer
- **默认**: 95
- **范围**: 50-99
- **描述**: 当磁盘使用率超过 N% 时开始清理
- **示例**: `90`, `95`, `98`

## 合并配置

### `merge.enabled`
- **类型**: boolean
- **默认**: `false`
- **描述**: 启用片段合并功能

### `merge.check_interval`
- **类型**: string
- **默认**: `"1h"`
- **描述**: 检查合并候选者的频率
- **示例**: `"30m"`, `"1h"`, `"2h"`

### `merge.window_size`
- **类型**: string
- **默认**: `"1h"`
- **描述**: 片段合并的时间窗口（此窗口内的片段可以合并）
- **示例**: `"30m"`, `"1h"`, `"2h"`

### `merge.batch_limit`
- **类型**: integer
- **默认**: 200
- **描述**: 一次合并的最大片段数
- **示例**: `100`, `200`, `500`

### `merge.min_segment_age`
- **类型**: string
- **默认**: `"10m"`
- **描述**: 片段可以合并的最小时间
- **示例**: `"5m"`, `"10m"`, `"30m"`

### `merge.min_segments_to_merge`
- **类型**: integer
- **默认**: 3
- **描述**: 触发周期合并所需的最小片段数
- **示例**: `2`, `3`, `5`

### `merge.rolling_enabled`
- **类型**: boolean
- **默认**: `true`（仅在 `merge.enabled=true` 时生效）
- **描述**: 片段关闭后 debounce，把新段合进当前 UTC 小时文件

### `merge.rolling_debounce`
- **类型**: string
- **默认**: `"5s"`
- **描述**: 滚动合并在最后一次片段关闭后再等多久

## FTP 配置

FTP 当前仅支持明文密码认证，常规初始化保存的哈希密码无法用于 FTP 登录；使用前见 [FTP 指南](ftp-integration.md)。

### `ftp.enabled`
- **类型**: boolean
- **默认**: `true`
- **描述**: 启用 FTP 服务器

### `ftp.port`
- **类型**: integer
- **默认**: 2121
- **范围**: 1-65535
- **描述**: FTP 控制端口
- **示例**: `2121`, `2021`

### `ftp.passive_port_range`
- **类型**: string
- **默认**: `"2122-2140"`
- **描述**: 被动模式端口范围（开始-结束）
- **示例**: `"2122-2140"`, `"40000-40100"`

## MQTT 配置

### `mqtt.enabled`
- **类型**: boolean
- **默认**: `false`
- **描述**: 启用 MQTT 客户端进行基于触发器的录制

### `mqtt.broker`
- **类型**: string
- **必需**: 是（如果启用）
- **描述**: MQTT 代理地址
- **示例**: `"tcp://localhost:1883"`, `"tcp://192.168.1.100:1883"`

### `mqtt.topic`
- **类型**: string
- **必需**: 是（如果启用）
- **描述**: MQTT 主题前缀；NVR 实际订阅 `{topic}/trigger/+`
- **示例**: `"lalmax-nvr"`, `"home/security"`

### `mqtt.client_id`
- **类型**: string
- **默认**: 无；启用 MQTT 时请显式设置
- **描述**: MQTT 客户端标识符
- **示例**: `"lalmax-nvr"`, `"nvr-client-01"`

### `mqtt.username`
- **类型**: string
- **可选**: 是
- **描述**: MQTT 代理身份验证用户名
- **示例**: `"mqtt-user"`, `"admin"`

### `mqtt.password`
- **类型**: string
- **可选**: 是
- **描述**: MQTT 代理身份验证密码
- **示例**: `"mqtt-password"`

## WebDAV 配置

### `webdav.enabled`
- **类型**: boolean
- **默认**: `true`
- **描述**: 启用 WebDAV 服务器

### `webdav.path_prefix`
- **类型**: string
- **默认**: `"/dav"`
- **描述**: WebDAV 访问的 URL 路径前缀
- **示例**: `"/dav"`, `"/recordings"`

### `webdav.read_write`
- **类型**: boolean
- **默认**: `false`
- **描述**: 允许写入操作（PUT、MKCOL、DELETE 等）
- **示例**: `true`, `false`

## HLS 配置

HLS 播放与录像拉流相互独立：关闭 `hls.enabled` 或启用按需切片，都不会中断正在进行的录像 pull。

### `hls.enabled`
- **类型**: boolean
- **默认**: `false`
- **描述**: 是否启用 HLS / LL-HLS 播放协议。关闭后 API 返回 503，并立即停止后台切片（不重启 lalmax）
- **示例**: `true`, `false`

### `hls.on_demand`
- **类型**: boolean
- **默认**: `true`
- **描述**: 按需 HLS 切片。开启时，仅在有观众请求 m3u8/ts 时启动 TS muxer 或 LL-HLS session；关闭时，拉流建立后即持续切片（即使用户未观看）
- **示例**: `true`, `false`

### `hls.idle_timeout`
- **类型**: string（时长）
- **默认**: `"60s"`
- **描述**: 按需切片模式下，距上次 HLS 请求超过该时间后停止切片并清理临时 TS/fMP4 文件。不影响录像拉流
- **示例**: `"30s"`, `"60s"`, `"2m"`, `"5m"`

### `hls.write_buffer_size`
- **类型**: integer
- **默认**: 100
- **描述**: 每个流的异步帧缓冲区大小（帧数单位）
- **示例**: `40`, `100`, `200`

### `hls.segment_max_size_mb`
- **类型**: integer
- **默认**: 10
- **描述**: HLS 片段最大大小（兆字节）
- **示例**: `5`, `10`, `20`

### `hls.segment_count`
- **类型**: integer
- **默认**: 7
- **范围**: 3-10
- **描述**: 每个流的 HLS 片段数
- **示例**: `5`, `7`, `10`

### `hls.max_streams`
- **类型**: integer
- **默认**: 4
- **范围**: 1-20
- **RPi 限制**: 树莓派 3B 上最大 4
- **描述**: 最大并发 HLS 流数
- **示例**: `4`, `8`, `16`

## 小米配置

### `xiaomi.user_id`
- **类型**: string
- **必需**: 是（如果配置了小米摄像头）
- **描述**: 小米云账户用户 ID（认证后获得）
- **示例**: `"1234567890"`

### `xiaomi.token`
- **类型**: string
- **必需**: 是（如果配置了小米摄像头）
- **描述**: 小米 passToken API 访问令牌（通过 `/api/xiaomi/auth` 获得）
- **示例**: `"xiaomi_token_123"`

### `xiaomi.region`
- **类型**: string
- **默认**: `"cn"`
- **描述**: 小米云区域代码
- **选项**: `"cn"`, `"sg"`, `"de"` 等
- **示例**: `"cn"`, `"sg"`

## 可观察性配置

### `observability.log_level`
- **类型**: string
- **默认**: `"info"`
- **选项**: `"debug"`, `"info"`, `"warn"`, `"error"`
- **描述**: NVR 主进程日志级别；embedded 模式下也会同步到 lal/lalmax 内部日志（`lalmax.conf.json` 的 `lal.log.level`）。lal/lalmax 的日志会转发到 NVR 服务日志，并标记为 `lal/lalmax`；lal 原始 stdout 仍关闭，避免在 `logs/lalmax-nvr.log` 中重复输出。
- **示例**: `"debug"`, `"info"`, `"error"`

### `observability.log_format`
- **类型**: string
- **默认**: `"text"`
- **选项**: `"json"`, `"text"`
- **描述**: 日志输出格式
- **示例**: `"json"`, `"text"`

### `observability.enable_pprof`
- **类型**: boolean
- **默认**: `false`
- **描述**: 启用 pprof 调试端点进行性能分析
- **注意**: 生产环境中请谨慎使用

### `observability.otlp`

- **描述**: 可选的外部 OTLP/HTTP 导出配置。应用仅从本配置读取，不需要也不读取 `OTEL_*` 环境变量
- 本地调用链会写入应用数据库，默认保留 24 小时、最多 10000 条；超过任一限制后定时清理。写入通过异步队列执行，队列满时会丢弃新 span 并记录警告
- **`local_trace_enabled`**: 控制是否将新 span 保存到本地数据库，默认 `true`；可在 Web 设置的“调用链”页签中即时切换。关闭后已保存的调用链不会删除
- **`trace_ui_url_template`**: 可选的第三方 UI URL 模板，必须是 HTTP(S) URL 并包含 `{trace_id}`。例如 `http://localhost:16686/trace/{trace_id}`；内部页面可同时查看本地 span 和跳转到第三方 Trace UI
- **`enabled`**: 是否启用外部导出，默认 `false`；关闭时前端观测 API 仍正常工作
- **`endpoint`**: Collector 基础 URL，默认 `http://127.0.0.1:4318`。应用会自动追加 `/v1/traces`、`/v1/metrics` 和 `/v1/logs`
- **`service_name`**: OTel 服务名，默认 `lalmax-nvr`
- 每次进程启动会生成唯一的 `service.instance.id`，用于区分同一服务的不同实例和重启周期
- **`traces_enabled` / `metrics_enabled` / `logs_enabled`**: 分别控制链路、指标与结构化日志导出，默认均为 `true`；启用 OTLP 时至少开启一项
- **`timeout`**: 单次导出超时，默认 `10s`
- **`metrics_export_interval`**: 指标批量导出周期，默认 `30s`
- **`logs_export_interval`**: 日志批量导出周期，默认 `1s`
- **`headers`**: 可选的 HTTP 请求头映射，可用于 Collector 鉴权。请将包含令牌的配置文件按敏感文件保护
- traces 和 logs 共用 W3C Trace Context；请求日志会包含对应的 trace/span ID，便于把错误日志关联到请求链路
- 业务 span 覆盖摄像头新增、更新、启动和 ONVIF 准备，媒体拉流启动，录像计划协调、录像任务创建、连接重试与录像分段落库；后台录像器会保留启动时的 trace 上下文，但不受 HTTP 请求取消影响
- 业务 span 使用 `nvr.*` 属性关联 `camera.id`、`stream.id`、协议、编码、重试次数和停止原因。不会导出完整流地址、用户名或密码
- 使用 HTTP 模式调用外置 lalmax 时会创建客户端 span，并传播 `traceparent` / `tracestate`；只记录请求方法、主机、路径和状态码，不记录查询参数或 baggage。外置 lalmax 也需启用 OpenTelemetry HTTP 服务端埋点，才能把两端 span 合并为同一条 trace
- `embedded` 模式下，内置 lalmax 的控制 API、HLS/FMP4 和 WebRTC HTTP 请求会创建服务端 span；发布、订阅、拉流等生命周期事件会携带短事件 span 的 trace context，经内部 SSE 回到 NVR。长连接型 SSE 和 FLV 请求不创建覆盖整个连接的长 span
- 指标包含 API 请求延迟、状态码和响应大小，以及 Go goroutine、内存、GC 和调度指标
- Go OTLP logs SDK 仍处于实验阶段，升级 OpenTelemetry Go 依赖时需留意兼容性变化

## 媒体引擎配置

### `media.mode`
- **类型**: string
- **默认**: `"embedded"`
- **描述**: lalmax 通信模式

### `media.lalmax_http_addr`
- **类型**: string
- **默认**: `"http://127.0.0.1:12090"`
- **描述**: lalmax HTTP API 地址

### `media.lalmax_public_url`
- **类型**: string
- **默认**: 与 `media.lalmax_http_addr` 相同
- **描述**: 对外播放地址使用的 hostname。必须写成客户端能访问的主机名，否则 `rtsp://` URL 会是 `127.0.0.1`

### `media.rtmp_port` / `media.rtsp_port` / `media.http_port`
- **类型**: integer
- **描述**: 外部 lal 的协议端口（`media.mode: http` 时用于生成播放地址）。默认 RTMP **11935**、RTSP **15544**、HTTP-FLV/HLS-TS **18080**。embedded 模式的 RTMP 推流端口由 `rtmp.port` 设置；RTSP 和 HTTP 监听使用内嵌引擎默认端口。请把 `media.lalmax_public_url` 设成客户端可访问的主机名。

### RTSP 服务端鉴权

`media.rtsp_auth_enable` 控制是否对 RTSP 播放启用鉴权。要正常使用鉴权，需要同时配置 `media.rtsp_username` 和 `media.rtsp_password`；`media.rtsp_auth_method` 为 `0`（Basic）或 `1`（Digest）。配置加密密钥启用后，保存配置时会加密 RTSP 密码。

embedded 模式下，lalmax HTTP 端口（默认 `12090`）的直连播放入口，包括 WHEP、LL-HLS、HTTP-fMP4 和 HTTP-FLV 代理，使用 NVR 本地账号认证。同端口上的 lalmax 控制、统计、hook 和 ZLM 兼容 API 也需要 NVR 账号。若配置了 lalmax `CtrlAuthWhitelist` token，这些 API 请用 HTTP Basic Auth 传递 NVR 账号，因为 `token` 查询参数保留给控制 API token。播放器需支持 HTTP Basic Auth，或使用 NVR API 支持的 `token` 查询参数。若同时配置了 `auth.playback_tokens`，媒体播放令牌也通过 `?token=...` 传入，此时请用 HTTP Basic Auth 传递 NVR 账号，避免两个用途的令牌冲突。LL-HLS 清单会将媒体令牌传递给分片；LL-HLS、HTTP-fMP4、WHEP、Jessibuca 和 ZLM 兼容 WebRTC 播放都会校验公共的单流播放令牌。应用内 `/api/cameras/.../stream` 播放地址继续由 NVR API 认证保护。RTMP、SRT、独立 lal HTTP（默认 `18080`）及 RTSP 原生监听端口不经过这层 HTTP 认证；请分别配置协议鉴权。

## DLNA 配置

可选的局域网 MediaServer，默认关闭。`dlna.port` 默认 **8200**，与 `server.listen` 分开；`include_live` 和 `include_recordings` 默认开启。可用 `interface` 指定网卡、`allowed_cidrs` 限制客户端网段。`max_browse_count` 默认 **200**，`gop_cache` 默认 **1**。`max_media_viewers` 已提供配置项，但当前未用于限制连接数。使用方法见 [DLNA 指南](dlna.md)。

## IPTV 配置

IPTV 导入、浏览器代理播放无需启动媒体拉流；发布频道或按录像计划拉流要求 `media.mode: embedded`。`iptv.max_concurrent_pulls` 限制发布与录像共享的并发 HLS 拉流数，默认 **32**，范围 **1–128**。使用方法见 [IPTV 指南](iptv.md)。

## 流媒体配置

### `streaming.default_protocol`
- **类型**: string
- **默认**: `"hls"`
- **选项**: `webrtc`, `flv`, `ws-flv`, `hls`, `ll-hls`
- **描述**: 默认流媒体协议

### `streaming.preview_auto_stop_sec`
- **类型**: integer
- **默认**: `60`
- **描述**: 按需子码流拉流的空闲超时（秒）

### `streaming.webrtc`
```yaml
streaming:
  webrtc:
    enabled: true       # 默认 true
    max_viewers: 2      # 默认 2，范围 [1,10]
    idle_timeout: "60s" # 默认 60s
```

### `streaming.flv`
```yaml
streaming:
  flv:
    enabled: true        # 默认 true
    max_viewers: 10      # 默认 10，范围 [1,50]
    idle_timeout: "60s"  # 默认 60s
    gop_cache_size: 1    # 默认 1
```

## WebSocket 配置

```yaml
websocket:
  max_viewers: 10        # 最大并发观看数
  write_buf_size: 1024   # 写缓冲区大小
  idle_timeout: "60s"    # 空闲超时
```

## RTMP 推流配置

```yaml
rtmp:
  enabled: false         # 默认 false；启用后内嵌媒体引擎默认监听 11935
  port: 11935            # 可选，RTMP 推流端口
  stream_keys:           # 每个 stream_id 必须配置一个推流令牌
    cam1: "generate-a-long-random-token"
```

RTMP 发布必须在 URL 查询参数中携带对应的 `stream_key`，例如 `rtmp://host:11935/live/cam1?stream_key=<token>`。未配置令牌或令牌不匹配的发布会在建立媒体流前被拒绝。流管理 API 只为已配置令牌的流返回 RTMP 推流地址。配置加密功能启用且密钥可用时，令牌会加密保存；RTMP 本身不提供传输加密，请在可信网络或加密隧道中使用。

## SRT 推流配置

```yaml
srt:
  enabled: false         # 默认 false；启用后内嵌媒体引擎默认监听 19000
  port: 19000            # 可选，SRT 推流端口
  stream_keys:           # 每个 stream_id 必须配置一个推流令牌
    cam1: "generate-a-long-random-token"
```

SRT 发布应在 streamid 的 `u` 字段携带对应令牌：`#!::h=<stream_id>,m=publish,u=<token>`。未配置令牌或校验失败时会在接受发布连接前拒绝。流管理 API 只为已配置令牌的流生成 SRT 地址。配置加密功能启用且密钥可用时，令牌会加密保存。

## JT1078 推流配置

> **实验性功能**：JT1078/JT808 当前仅通过软件模拟终端和自动化测试验证，尚未使用真实车载终端验证。设备兼容性及实际运行稳定性仍需验证。

JT/T 1078 由内嵌 lalmax 在 **TCP 与 UDP `:1078`** 收流，按包头 `SIM + 逻辑通道号` 分流，流名为 `{sim}_{channel}`，再 `FeedAvPacket` 进 group。视频支持 H.264 / H.265，音频支持 AAC / G.711A / G.711U。MP3 和 G.726 会丢掉，不送进 lal。embedded 模式首次生成或缺少该字段时默认开启。UDP 流无连接状态，空闲 15 秒后回收。`phone_len` 为 0 时自动识别 2016（6 字节）和 2019（10 字节）手机号，也可固定为 6 或 10。

在 `lalmax.conf.json` 中配置：

```json
{
  "lalmax": {
    "jt1078_config": {
      "enable": true,
      "addr": ":1078",
      "udp_enable": true,
      "udp_addr": ":1078",
      "udp_idle_timeout_ms": 15000,
      "phone_len": 0,
      "allowed_streams": ["1003_1"]
    }
  }
}
```

JT808 信令服务在 `lalmax-nvr.yaml` 中单独启用（默认关闭）：

```yaml
jt808:
  enabled: true
  port: 808
  media_ip: "192.168.1.100" # 必须是终端可达地址
  auth_code: "set-a-private-jt808-secret" # 注册时下发，0102 鉴权时校验
  media_tcp_port: 1078
  media_udp_port: 1078
  transport: tcp            # tcp 或 udp，影响 0x9101 / 0x9201 的端口字段
  timeout: "3s"
  # ftp_port: 2121          # 可选，0x9206 默认用 ftp.port
  # ftp_user: ""            # 可选，默认 auth.username
  # ftp_password: ""        # 可选明文；只配了 password_hash 时上传请求必须自带密码
```

JT1078 发布采用精确匹配的 `{SIM}_{通道}` 白名单。启用 JT808 时，已通过直播或回放指令授权的流也允许发布；关闭 JT808 时，只允许 `allowed_streams` 中配置的 ID，空白名单会拒绝所有 JT1078 发布。该字段配置在 `lalmax.conf.json`。

终端注册/鉴权后，已鉴权接口如下；所有 POST 接口还要求 super_admin 操作权限。播放地址走现有 HLS / FLV / WebRTC，例如 `http://host:18080/live/1003_1.flv`。

- `GET /api/jt808/terminals` 列出终端。
- `POST /api/jt808/play` 下发 0x9101。`data_type`：0 音视频、1 视频、2 对讲、3 监听、4 广播。`stream`：0 主码流、1 子码流。
- `POST /api/jt808/stop` 关闭该通道音视频。`POST /api/jt808/control` 发送完整 0x9102：`control` 0 关闭、1 切码流、2 暂停、3 恢复、4 关对讲；`close_av` 0 全部、1 音频、2 视频。
- `POST /api/jt808/loss` 发送 0x9105，`rate` 为 0–100。
- `GET /api/jt808/av?key=1003` 查询 0x9003。
- `POST /api/jt808/playback` 与 `POST /api/jt808/playback/control` 对应 0x9201 / 0x9202。时间用 RFC3339 或 `2006-01-02 15:04:05`。拖动时 `control` 为 5，并带 `drag_to`。
- `POST /api/jt808/resources` 查询 0x9205。
- `POST /api/jt808/upload` 与 `POST /api/jt808/upload/control` 对应 0x9206 / 0x9207。FTP 主机、端口、账号为空时使用本机 FTP 配置；`jt808.ftp_port`、`ftp_user`、`ftp_password` 可单独覆盖。只配置了密码哈希、没有明文密码时，请求里必须自己带密码。
- `POST /api/jt808/ptz` 的 `action` 为 `rotate`、`focus`、`iris`、`wiper`、`infrared`、`zoom`。
- `POST /api/jt808/downlink` 在终端 TCP 媒体连接已建立时下发对讲音频。`payload` 为 base64，`pt` 只接受 6（G.711A）、7（G.711U）、19（AAC）。

没有自动摄像头建档，也没有 JT808 页面。定位、报警、电子围栏和加密帧不在这组接口里。

### 模拟验证与已知限制

在仓库根目录执行 `go test ./internal/jt808 ./tools/jtsim`，在 `third/lalmax` 目录执行 `go test ./jt1078 ./config`。模拟器覆盖 TCP/UDP 接入、2013/2019 报文头，以及未请求媒体和停止后媒体的拒绝行为。回归测试覆盖标准 0x1003 响应、授权清理、关闭连接和下行序号。

媒体重启功能测试通过，但扩展的重启 race 测试仍报告底层 lal 日志初始化及 HTTP 启停的既有竞争，尚未修复。当前尚未使用真实设备验证。

## WHIP 推流配置

```yaml
whip:
  enabled: true          # 默认 true；关闭只隐藏接入 URL，不影响 WHEP 播放
  stream_keys:           # camera_id → streamid 映射（可选）
    cam1: "obs-front"
  publish_tokens:        # stream_id → publish token
    cam1: "generate-a-long-random-token"
```

WHIP 发布地址携带 `stream_key` 查询参数，或使用 `Authorization: Bearer <token>` 请求头；API 返回的地址会附带令牌。仅接受为该 stream ID 配置的令牌，鉴权在创建媒体会话前执行。配置加密功能启用且密钥可用时，令牌会加密保存。ICE 复用端口 **4888/udp**（及 TCP）需对推流端可达。Docker 请映射 `12090` 与 `4888`。`media.lalmax_public_url` 必须是对外 hostname。

## 健康监控配置

```yaml
health:
  enabled: true
  events_retention: "168h"       # 事件保留时长（7天）
  alerts:
    cooldown: "5m"               # 告警冷却时间
    mqtt: false                  # 是否通过 MQTT 发送告警
  layer1:                        # 连接健康
    offline_threshold: "5m"      # 离线判定阈值
  layer2:                        # 流质量
    bitrate_change_threshold: 0.5
    min_fps: 5
    max_idr_interval: "10s"
  layer2_5:                      # 画面冻结
    freeze_timeout: "30s"
  auto_remediation:
    enabled: false               # 自动重启故障摄像头
    max_restarts_per_hour: 3
    cooldown_minutes: 10
    blacklist_hours: 1
    global_max_per_min: 5
  rediscovery:
    enabled: true                # 按 ONVIF 序列号单播自愈 IP
    max_parallel: 16
    probe_timeout: "2s"
```

## ONVIF 即插即用

默认关闭。开启后监听 WS-Discovery Hello 并周期 Probe，自动登记新摄像头（无凭据时进入待激活）。

```yaml
auto_discover:
  enabled: false
  listen_for_hello: true
  scan_interval: 60s
  default_username: ""
  default_password: ""
```

## 事件录像

流的录像计划 `mode` 为 `event`（MQTT / ONVIF 移动侦测）或 `adaptive`（平静时稀疏关键帧）时生效。计划挂在 `stream_id` 上，见 [录像计划](recording-plans.md)。

```yaml
event:
  post_roll: 30s
  max_duration: 5m
```

每路还可配置 `adaptive.timelapse_interval`（默认 `30s`）作为平静时的关键帧间隔。

## 远程日志配置

```yaml
remote_log:
  enabled: false
  endpoint: "http://localhost:9428/insert/jsonline"  # VictoriaLogs 地址
  format: "jsonline"             # jsonline 或 loki
```

## Metrics 认证配置

```yaml
metrics_auth:
  username: "metrics_user"
  password: "metrics_password"
  # 或使用密码哈希：
  # password_hash: "$2a$10$..."
```

> 配置了 `username` 和 `password`（或 `password_hash`）时，`/metrics` 使用这组 Basic Auth 凭据；未配置时使用 NVR API 凭据。`/metrics` 不再公开访问。

## AI 检测配置

```yaml
ai:
  enabled: false                 # 启用 AI 检测
  backend: "browser"             # browser（浏览器端 ONNX）/ webhook（第三方检测器）/ disabled
  model_url: "/models/yolo11n.onnx" # 浏览器端模型 URL；模型文件放到数据目录/models/
  inference_timeout_ms: 30000    # 推理超时（默认 30000ms）
  frame_skip_rate: 3             # 每 N 帧检测一次（默认 3）
  confidence_threshold: 0.3      # 置信度阈值（默认 0.3）
```

浏览器端推理在 WebCodecs 播放器中运行，优先使用 WebGPU 并回退到 WASM。第三方模式保留 `POST /api/ai/webhook` 接口，适用于独立检测服务订阅摄像头 RTSP 流。

## 摄像头协议示例

### RTSP 摄像头
```yaml
cameras:
  - id: "front-door"
    name: "前门摄像头"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    username: "admin"
    password: "摄像头密码"
    enabled: true
    sub_stream_url: "rtsp://192.168.1.100:554/stream2"
    snapshot_url: "http://192.168.1.100:8080/snapshot"
```

### HTTP JPEG 摄像头
```yaml
cameras:
  - id: "backyard"
    name: "后院摄像头"
    protocol: "http"
    encoding: "jpeg"
    url: "http://192.168.1.101/capture"
    sample_interval: 1
    enabled: true
```

### ONVIF 摄像头
```yaml
cameras:
  - id: "lobby"
    name: "大厅摄像头"
    protocol: "onvif"
    url: "http://192.168.1.102:80/onvif/device_service"
    enabled: true
    # 可选：指定编码
    encoding: "h264"
    # 可选：指定流编码
    stream_encoding: "H264"
```

### 小米摄像头
```yaml
xiaomi:
  user_id: "1234567890"
  token: "xiaomi_token_123"
  region: "cn"

cameras:
  - id: "xiaomi-cam"
    name: "小米摄像头"
    protocol: "xiaomi"
    encoding: "h264"
    did: "xiaomi_device_id"
    vendor: "cs2"
    enabled: true
```

## 从旧格式迁移

旧协议格式如 `"rtsp_h264"` 会自动转换为新的独立 `protocol` 和 `encoding` 字段：

```yaml
# 旧格式（仍然支持）
cameras:
  - id: "cam1"
    protocol: "rtsp_h264"
    url: "rtsp://..."

# 自动转换为新格式：
# protocol: "rtsp"
# encoding: "h264"
```

## 验证规则

配置在启动时会根据以下约束进行验证：

- **摄像头 ID**: 在所有摄像头中必须唯一
- **摄像头地址**: 必须有有效的协议（http/rtsp）和主机
- **ONVIF 摄像头**: 必须有 URL 或 onvif_endpoint
- **小米摄像头**: 必须配置 xiaomi.token
- **端口号**: 必须在 1-65535 范围内
- **片段时长**: RPi 3B 上最大 30 秒
- **保留天数**: 必须在 1-3650 之间
- **磁盘阈值**: 必须在 50% 到 99% 之间
- **合并配置**: 所有时间段字段必须有效
- **HLS 配置**:
  - 片段数: 3-10
  - 最大流数: 1-20（RPi 3B 上为 4）
  - `idle_timeout`: 必须为有效正时长

## 文件路径和位置

- **默认配置路径**: `./lalmax-nvr.yaml`
- **默认存储**: `/var/lib/lalmax-nvr`
- **录像**: `{root_dir}/recordings/{encoding}/{camera_id}/`
- **片段**: `{root_dir}/recordings/{encoding}/{camera_id}/`
- **快照**: `{root_dir}/snapshots/{camera_id}/`
- **WebDAV**: `{root_dav}{root_dir}/`（其中 root_dav 是反向代理路径）

## 快速配置

### 基本设置
```yaml
server:
  listen: ":9090"
storage:
  root_dir: "/var/lib/lalmax-nvr"
  segment_duration: "30s"
auth:
  username: "admin"
  password: "你的密码"
cameras:
  - id: "cam1"
    name: "摄像头 1"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
cleanup:
  retention_days: 30
  disk_threshold_percent: 95
```

### 包含所有功能的完整设置
```yaml
server:
  listen: ":9090"
storage:
  root_dir: "/mnt/data/nvr"
  segment_duration: "30s"
auth:
  username: "admin"
  password_hash: "$2a$10$N9qo8uLOickgx2ZMRZoMy..."
cameras:
  - id: "front-door"
    name: "前门"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
    sub_stream_url: "rtsp://192.168.1.100:554/sub"
  - id: "xiaomi-cam"
    name: "小米摄像头"
    protocol: "xiaomi"
    encoding: "h264"
    did: "xiaomi_device_id"
    vendor: "cs2"
    enabled: true
xiaomi:
  user_id: "1234567890"
  token: "xiaomi_token_123"
  region: "cn"
cleanup:
  retention_days: 30
  disk_threshold_percent: 90
merge:
  enabled: true
  check_interval: "1h"
  batch_limit: 200
ftp:
  enabled: true
  port: 2121
mqtt:
  enabled: true
  broker: "tcp://192.168.1.100:1883"
  topic: "lalmax-nvr"
webdav:
  enabled: true
  read_write: false
hls:
  max_streams: 4
observability:
  log_level: "info"
  trace_retention: "24h"
  max_local_traces: 10000
  trace_ui_url_template: "http://localhost:16686/trace/{trace_id}"
  otlp:
    enabled: true
    endpoint: "http://otel-collector:4318"
    service_name: "lalmax-nvr"
    traces_enabled: true
    metrics_enabled: true
    logs_enabled: true
    metrics_export_interval: "30s"
    logs_export_interval: "1s"
```
