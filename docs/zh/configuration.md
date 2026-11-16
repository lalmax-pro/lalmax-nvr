# NVR-04 配置规范

## 1. 事实源与配置原则

配置字段定义、默认值和启动校验分别位于：

- `internal/config/config.go`：`Config`、各模块结构、`Load`、`ApplyDefaults`、`Validate`。
- `internal/config/config_p0.go`：自动发现、事件、自适应和部分健康配置。
- `internal/transcode/config.go`、`internal/voip/config.go`：转码与 VoIP 专用配置。
- [`config/config.example.yaml`](../../config/config.example.yaml)：完整 YAML 示例和协议说明。

**CFG-001｜新增字段必须三处同步。** 字段结构、默认值/校验、示例和本规范应保持一致；配置结构体中的注释不能替代对实际启动路径的核实。

**CFG-002｜零值不总是“关闭”。** 指针布尔值用于区分“未设置”和“显式 false”。例如 FTP/WebDAV 默认开启，WHIP 默认开启，HLS/RTMP/SRT/GB28181/DLNA 默认关闭。普通布尔字段通常无法区分未设置和显式 false。

**CFG-003｜不要假设未知字段会被拒绝。** 当前 YAML 加载使用非严格 `yaml.Unmarshal`，不能依赖它发现所有拼写错误。配置评审和启动后行为检查必须覆盖实际字段名。

## 2. 文件路径与初始化

| 场景 | 默认配置路径 | 默认数据目录 |
|------|--------------|--------------|
| 直接运行主程序 | `config/lalmax-nvr.yaml` | `/var/lib/lalmax-nvr` |
| `init` 子命令 | 当前目录 `lalmax-nvr.yaml`，必须用 `--config` 才能与主程序默认路径一致 | `/var/lib/lalmax-nvr` |
| Unix 管理脚本 | 仓库 `config/lalmax-nvr.yaml` | 仓库 `data/`，脚本会同步 `root_dir` |
| Docker 镜像 | `/data/lalmax-nvr.yaml` | `/data` |

主程序找不到配置时会自动生成默认配置；不是必须手工复制示例才能启动。自动生成时：

- `NVR_DATA_DIR` 决定数据目录；未设置且存在 `/data` 时使用 `/data`。
- `NVR_PASSWORD` 可设置初始密码，至少 8 字符；未设置则进入首次设置状态。
- 在 Docker 环境中，若 `storage.root_dir` 仍为非 Docker 默认值或空值，启动会改为 `NVR_DATA_DIR` 指向的目录。

**CFG-101｜环境变量不是全量覆盖层。** `NVR_PASSWORD` 用于自动初始化，不应被描述为每次启动都覆盖已有 YAML 密码；`NVR_DATA_DIR` 也不会无条件覆盖任意显式自定义的 `root_dir`。需要固定行为时应直接管理配置文件。

常用 CLI：

```bash
lalmax-nvr -config config/lalmax-nvr.yaml
lalmax-nvr -version
lalmax-nvr health --config config/lalmax-nvr.yaml
lalmax-nvr init --password 'replace-me' --config config/lalmax-nvr.yaml --data-dir /var/lib/lalmax-nvr
lalmax-nvr hash-password 'replace-me'
lalmax-nvr encrypt-config --config config/lalmax-nvr.yaml
```

## 3. 加载、校验与保存

启动顺序为：读取 YAML → 应用默认值 → 可用时解密敏感字段 → `Validate` → 初始化数据库和各模块。配置校验失败会阻止启动。

**CFG-201｜时长使用 Go duration。** 字符串时长采用 `300ms`、`30s`、`5m`、`24h` 等格式。例外是 `websocket.idle_timeout` 直接映射 `time.Duration`，YAML 显式值应按整数纳秒理解，例如 60 秒为 `60000000000`。

**CFG-202｜保存不保留 YAML 摄像头列表。** `config.Save` 会把 `cameras` 置空；设备存储在数据库中。旧 YAML 设备条目用于启动时同步/迁移，不应再把 YAML 当作设备主数据库。

**CFG-203｜保存采用临时文件和重命名。** Web 设置更新涉及敏感字段时，可在有密钥的情况下加密保存；进程内恢复为明文供运行使用。密钥规则见[安全规范](security.md)。

**CFG-204｜外部修改需要显式处理。** Watcher 通过修改时间和文件大小检测变化。`GET /api/config/check` 只检测；`POST /api/config/reload` 重载并更新当前 API 配置及已接入的特定状态。该操作不校验并重建所有模块，也不等于重启所有监听器。涉及监听地址、数据库、媒体引擎、SIP、FTP 等结构变化时应重启进程，并以其专用更新接口的实际行为为准。

## 4. 最小生产配置

```yaml
server:
  listen: ":9090"
storage:
  root_dir: "/var/lib/lalmax-nvr"
  segment_duration: "30s"
media:
  mode: "embedded"
  lalmax_http_addr: "http://127.0.0.1:12090"
  lalmax_public_url: "http://nvr.example.com:12090"
auth:
  username: "admin"
  password_hash: "use-a-generated-bcrypt-hash"
cleanup:
  retention_days: 30
  check_interval: "1h"
  disk_threshold_percent: 95
ftp:
  enabled: false
webdav:
  enabled: true
  read_write: false
```

该示例强调最小安全配置，不替代完整示例。`lalmax_public_url` 应按实际播放拓扑设置；若浏览器只经 `9090` 同源代理，可不必向公众开放 `12090`。

## 5. 核心配置

### 5.1 `server` 与 `storage`

| 字段 | 类型/默认值 | 规则 |
|------|-------------|------|
| `server.listen` | string，`:9090` | Web/API 监听地址；改端口时同步健康检查和代理 |
| `storage.root_dir` | string，`/var/lib/lalmax-nvr` | 文件和默认 SQLite 根目录；必须可写且持久化 |
| `storage.segment_duration` | duration，`30s` | 启动校验范围 `1s–30m`；实际切分等待关键帧 |
| `storage.database_driver` | 空/`sqlite` 语义；另支持 `postgres`、`mysql` | 与 DSN 配合使用 |
| `storage.database_dsn` | 空时使用 `{root_dir}/lalmax-nvr.db` | 外部 DB DSN 属敏感信息 |
| `storage.database_migration_target` | 可选对象 | 仅测试/迁移目标，不自行改变活动数据库 |

**CFG-301｜片段时长不是固定内存估算公式。** 当前 MP4 实现会写媒体数据并维护封装元数据；不得沿用旧文档中“整段视频全部保留在 RAM”及固定 MB/秒的容量承诺。应通过目标码率、路数和设备实测评估资源。

数据库迁移与权限见[数据库配置与迁移](database-migration.md)。

### 5.2 `media`

| 字段 | 默认值/选项 | 规则 |
|------|-------------|------|
| `mode` | `embedded` | 允许 `embedded` / `http` |
| `lalmax_http_addr` | `http://127.0.0.1:12090` | NVR 到引擎的地址，必填 |
| `lalmax_public_url` | 默认等于内部地址 | 对外生成 URL 的基地址，远程客户端必须可达 |
| `lalmax_config_path` | `{root_dir}/config/lalmax.conf.json` | embedded 引擎生成配置路径 |
| `http_play_auth_enabled` | true | 控制 embedded 直连 HTTP 播放的 NVR 账号认证 |
| `rtmp_port` / `rtsp_port` / `http_port` | 媒体适配器默认 `11935` / `15544` / `18080` | 主要用于播放 URL；embedded RTMP 监听端口由 `rtmp.port` 管理 |
| `rtmp_listen_addr` | 空 | 可限制 embedded RTMP 监听主机 |
| `rtsp_auth_enable` | false | 原生 RTSP Basic/Digest 开关 |
| `rtsp_auth_method` | 0 | `0=Basic`、`1=Digest` |
| `rtsp_username/password` | 空 | 启用原生认证时必须配置 |
| `rtsp_publish_auth_enabled` | true | 是否把共享发布令牌策略用于 RTSP 发布；false 仍保留封禁检查 |

### 5.3 `auth` 与 `metrics_auth`

| 字段 | 规则 |
|------|------|
| `auth.username` | 旧全局账号和 FTP/WebDAV 账号；默认初始化常用 `admin` |
| `auth.password_hash` | bcrypt 哈希，优先于明文 |
| `auth.password` | 可用于初始化；常规启动哈希后清除 |
| `auth.publish_tokens` | `stream_id → 共享发布令牌`，可用于适用媒体发布 |
| `auth.playback_tokens` | `stream_id → 共享播放令牌`，配置后按流要求令牌 |
| `metrics_auth.username` + `password_hash` 或 `password` | 配置完整时用于 `/metrics` 独立 Basic Auth |

`/metrics` 未配置独立凭据时使用全局 NVR 认证中间件，不是公开端点。配置结构注释若与此不一致，以 `cmd/lalmax-nvr/main.go` 的顶层路由为准。

### 5.4 `cameras`

设备应通过 Web UI/API 管理并保存到数据库。核心字段：

| 字段 | 规则 |
|------|------|
| `id` | 必填、非空且设备间唯一；推荐使用稳定 ASCII slug |
| `name` | 展示名称 |
| `protocol` + `encoding` | 组合约束见[媒体与流](media-sources.md) |
| `url` | 除 ONVIF、小米、GB28181 等例外，必须有 scheme 和 host |
| `username/password` | 设备凭据，API 不应回传密码 |
| `enabled` | 控制设备生命周期，不等于录像计划 |
| `stream_id` | 可选绑定流；为空通常使用设备 ID |
| `rtsp_transport` | `tcp` / `udp`，默认 `tcp` |
| `onvif_endpoint/profile_token/stream_encoding` | ONVIF 端点、主码流 profile 和编码 |
| `sub_stream_url/sub_profile_token` | 可选预览子码流 |
| `snapshot_url` | 可选 HTTP JPEG 快照来源 |
| `audio_enabled` | YAML 加载时支持的 H.264/H.265 设备默认开启；JPEG/MJPEG 强制不支持 |
| `sample_interval/hls_max_fps` | MJPEG 采样和 HLS 帧率相关控制 |
| `frame_watchdog_timeout` | 可选帧看门狗时长 |
| `pull_retry_num` | `-1` 持续、0 不额外重试、正数为限定次数；具体来源默认可能不同 |
| `merge` / `health_overrides` | 设备级非零覆盖 |
| `adaptive.timelapse_interval` | 自适应平静期 IDR 间隔，默认 30s，有效实现限制 5s–10m |
| `timelapse` | 可选延时摄影：interval、output_fps、video_codec、delete_original |
| `did/vendor` | 小米设备 ID 和传输厂商 |

旧组合协议值仍可解析，但新文档和新集成必须使用分离字段。

### 5.5 `cleanup` 与 `merge`

| 字段 | 默认值 | 启动/行为约束 |
|------|--------|---------------|
| `cleanup.retention_days` | 30 | `1–3650`；0 会回退默认，不表示永久 |
| `cleanup.check_interval` | `1h` | 清理周期 |
| `cleanup.disk_threshold_percent` | 95 | `50–99`；超过阈值触发最早录像清理 |
| `merge.enabled` | false | 合并总开关 |
| `merge.check_interval` | `1h` | 启用时必须是合法 duration |
| `merge.window_size` | `1h` | 合并时间桶；运行辅助方法对非法值有回退 |
| `merge.batch_limit` | 200 | 启用时必须为正 |
| `merge.min_segment_age` | `10m` | 周期候选最小年龄 |
| `merge.min_segments_to_merge` | 3 | 启用时至少 2 |
| `merge.rolling_enabled` | true | 仅在总开关开启时生效 |
| `merge.rolling_debounce` | `5s` | 滚动合并等待；配置非法时运行辅助方法回退 5s |

清理的锁定、纯流和归档例外见[录像与回放](recording-plans.md)。

## 6. 播放与流控

### 6.1 `hls`

| 字段 | 默认值/约束 |
|------|-------------|
| `enabled` | false；HLS/LL-HLS 总开关 |
| `on_demand` | true；仅访问时切片 |
| `idle_timeout` | `60s`；必须为正 duration |
| `write_buffer_size` | 100；示例中的 40 是覆盖值，不是默认值 |
| `segment_max_size_mb` | 10 |
| `segment_count` | 7；启动范围 3–10，LL-HLS 下至少调整为 7 |
| `max_streams` | 4；启动范围 1–20 |
| `low_latency` | 当前 `ApplyDefaults` 强制 true，显式 false 也会变 true |
| `part_min_duration` | `200ms`；允许 100ms–1s |
| `lal_fragment_duration_ms` / `lal_fragment_num` | 3000 / 6 |
| `lal_cleanup_mode` | 当前强制为 2（ASAP），其他值会被覆盖 |
| `lal_use_memory` / `lal_temp_dir` | false / `hls-temp` |
| `lalmax_segment_duration` / `lalmax_part_duration` | 1 秒 / 200 毫秒 |

### 6.2 `streaming` 与 `websocket`

| 字段 | 默认值/约束 |
|------|-------------|
| `streaming.default_protocol` | `webrtc`；允许 webrtc/flv/ws-flv/hls/ll-hls；HLS 关闭时 hls/ll-hls 回退 webrtc |
| `streaming.auto_stop_no_view_sec` | 300；当前 0 也会被改为 300，不能按旧注释写成“0 禁用” |
| `streaming.preview_auto_stop_sec` | 60 |
| `streaming.webrtc.enabled/max_viewers/idle_timeout` | true / 2 / 60s；观众数 1–10 |
| `streaming.flv.enabled/max_viewers/idle_timeout/gop_cache_size` | true / 10 / 60s / 1；观众数 1–50 |
| `websocket.max_viewers/write_buf_size/idle_timeout` | 10 / 100 / 60s；YAML idle_timeout 显式值为整数纳秒 |

## 7. 推流与设备协议

### 7.1 RTMP / SRT / WHIP

```yaml
rtmp:
  enabled: true
  port: 11935
  publish_auth_enabled: true
  stream_keys:
    front-door: "replace-with-a-long-random-token"
srt:
  enabled: true
  port: 19000
  stream_keys:
    front-door: "replace-with-a-different-token"
whip:
  enabled: true
  stream_keys:
    front-door: "optional-stream-alias"
  publish_tokens:
    front-door: "replace-with-a-whip-token"
```

- RTMP 地址使用 `?stream_key=<token>`；SRT `streamid` 使用 `u=<token>`；WHIP 可使用相应查询令牌或 Bearer 方式，以生成地址和实现为准。
- 禁用 WHIP 只关闭接入广告/映射，不关闭 WHEP 播放。
- 凭据必须按流独立生成；匿名发布风险见[安全规范](security.md)。

### 7.2 GB28181

| 字段 | 默认值/规则 |
|------|-------------|
| `enabled` | false |
| `port` | 5060 |
| `id` | 默认示例平台 ID；生产必须分配符合部署规划的 20 位平台 ID |
| `password` | 设备注册 Digest 密码，应显式配置 |
| `media_ip` | 设备可达的 NVR 媒体接收 IP |
| `media_port` | 0 为自动/多端口；大于 0 为单端口模式 |
| `host` | 空时按 `media_ip` 等自动确定 |
| `standard_version` | `2016`，可选 `2022` |
| `whep_on_demand_enabled` | false；允许已知在线通道的 WHEP 按需 INVITE，受实现条件约束 |

GB28181 是设备 REGISTER、平台 INVITE、设备推 PS/RTP，不是 NVR 拉设备 RTSP。操作见[GB28181 指南](gb28181-guide.md)。

### JT1078 / JT808（实验性）

`jt808` 默认关闭。主要字段为 `port=808`、`media_ip`、`auth_code`、`media_tcp_port=1078`、`media_udp_port=1078`、`transport=tcp`、`timeout=3s` 及可选 FTP 覆盖。

JT1078 媒体监听位于生成的 lalmax 配置的 `jt1078_config`，embedded 默认 TCP/UDP `:1078`。流名为 `{sim}_{channel}`，发布受精确白名单或 JT808 授权控制。当前仅有模拟器/自动化验证，不得承诺真实车载终端兼容性；详细字段见[完整示例](../../config/config.example.yaml)。

## 8. 集成模块

| 模块 | 默认状态 | 关键配置 |
|------|----------|----------|
| `ftp` | **开启**，2121，passive 2122–2140 | 全局账号支持哈希校验；明文 FTP 且可写，不需要时关闭 |
| `webdav` | **开启**，`/dav`，只读 | `read_write=true` 才开放写方法 |
| `mqtt` | 关闭 | broker、`topic` 前缀、client_id、账号；订阅 `{topic}/trigger/+` |
| `auto_discover` | 关闭 | Hello 默认随启用开启；scan_interval 默认 60s，运行最小 30s；可指定网卡 |
| `dlna` | 关闭 | HTTP 8200；直播/录像默认包含；CIDR、gop_cache 1–16、browse 1–1000 |
| `iptv.max_concurrent_pulls` | 32 | 发布/录像共享 HLS 拉流上限；默认化将大于 128 的值截为 128 |
| `xiaomi` | 无默认账号 | 默认 region `cn`；支持默认账号和多账号映射 |
| `snapshot` | 关闭 | 默认 interval 5m、quality 80、max_age 24h；最小采集间隔由快照管理器约束 |
| `ai` | 关闭，backend `disabled` | browser/webhook；模型 `/models/yolo11n.onnx`，跳帧 3、阈值 0.3、超时 30000ms |

DLNA 的 `max_media_viewers` 默认 4，但当前配置示例明确标为保留字段，不能声称已强制限流。ONVIF、MQTT、AI 和 Home Assistant 的完整步骤见对应专题。

## 9. 健康、事件与快照

`health.enabled` 默认 **false**。启用后默认值包括：

| 字段 | 默认值 |
|------|--------|
| `events_retention` | `720h` |
| `alerts.cooldown` / `alerts.mqtt` | `5m` / false |
| `layer1.offline_threshold` | `30s` |
| `layer2.bitrate_change_threshold` / `min_fps` / `max_idr_interval` | 0.5 / 5 / `60s` |
| `layer2_5.freeze_timeout` | `10s` |
| `auto_remediation.enabled` | false |
| `max_restarts_per_hour` / `cooldown_minutes` / `blacklist_hours` | 3 / 5 / 1 |
| `global_max_per_min` / `offline_restart_seconds` | 10 / 90 |
| `rediscovery.enabled` / `max_parallel` / `probe_timeout` | true / 16 / `2s` |

自动修复启用时还会校验每路重启次数、冷却和离线重启阈值。`event.post_roll=30s`、`event.max_duration=5m`；自适应配置规则见[录像与回放](recording-plans.md)。

## 10. 可观测性

| 字段 | 默认值/约束 |
|------|-------------|
| `observability.log_level` | `info`；debug/info/warn/error |
| `log_format` | `text`；json/text |
| `enable_pprof` | false；生产不应无条件公开 |
| `trace_retention` / `max_local_traces` | `24h` / 10000 |
| `local_trace_enabled` | true；可在 Web 设置切换，新开关不删除旧 trace |
| `trace_ui_url_template` | 空；非空必须是含 `{trace_id}` 的 HTTP(S) URL |
| `otlp.enabled` | false |
| `otlp.endpoint` / `service_name` | `http://127.0.0.1:4318` / `lalmax-nvr` |
| `traces_enabled` / `metrics_enabled` / `logs_enabled` | true / true / true；OTLP 开启时至少一项 |
| `timeout` / `metrics_export_interval` / `logs_export_interval` | 10s / 30s / 1s |
| `otlp.headers` | 可选认证头，属敏感配置 |
| `remote_log.enabled` | false；开启必须有 endpoint |
| `remote_log.format` | `jsonline`，另支持 `loki` |

OTLP 使用应用 YAML，不应声称读取 `OTEL_*` 环境变量。Collector endpoint 是基础 URL，应用追加各 signal 的 `/v1/...` 路径。

## 11. VoIP、转码与 SFU

- `voip.enabled=false`；要求 `media.mode=embedded`。默认 UDP `0.0.0.0:5070`、RTP `41000–42000`、ring 30000ms、RTP timeout 15000ms、ACK timeout 32000ms。PBX、TLS、SRTP、用户及自动录制约束见[VoIP](voip.md)。
- `transcoding.enabled=false`、`worker_enabled=false` 零值语义；`max_concurrent=2`、`threads_per_job=2`，校验上限分别 32 和 16。FFmpeg/FFprobe/GStreamer 路径为空时按工具名解析。当前字段不是旧文档中的 `max_workers`、`replace_original` 等；见[转码部署](transcoding-deployment.md)。
- `sfu.enabled=false`；仅 embedded，属于实验能力。`idle_timeout` 空表示不自动回收；`pacing=true` 必须同时 `observe_bandwidth=true`。TURN URL 必须配置凭据，ICE 数量和长度有校验。

## 12. 配置评审清单

1. 从当前示例复制字段，检查是否存在拼写错误、旧字段和重复模块。
2. 核对指针布尔的“未设置默认”与显式 false，不凭普通布尔经验推断。
3. 核对所有时长格式、端口、URL、来源协议与编码组合。
4. 生产环境关闭未使用的 FTP、推流、SIP、JT 和发现服务。
5. 配置变化涉及监听器、数据库或引擎时安排重启，并验证真实行为。
6. 验证认证、播放、推流、录像和就绪探针，而不是仅以“进程启动”为验收。
7. 不提交真实配置和密钥；备份配置时同时制定密钥恢复方案。
