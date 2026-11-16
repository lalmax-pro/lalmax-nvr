# NVR-02 媒体与流规范

## 1. 目的

本文统一规定来源配置、运行流、设备身份和播放之间的关系。Web 的 **媒体接入** 是操作入口，不代表所有来源共用一种内部协议。

## 2. 对象模型

**MEDIA-001｜区分四个层次。**

| 层次 | 示例 | 生命周期 |
|------|------|----------|
| 来源配置 | RTSP URL、M3U 订阅、Emby 服务 | 保存配置，不保证有媒体 |
| 内容项 | IPTV 频道、Emby 电影/剧集 | 可探测、纳管或发布 |
| 运行流 | `stream_id` 对应的实时媒体组 | 随发布、拉流或推流上下线 |
| 设备与计划 | Camera、RecordingPlan | 分别负责运维身份和写盘策略 |

来源配置存在、内容可浏览、流在线和录像写盘不得互相推导。只有运行流详情、编码和状态可用于判断实时能力。

**MEDIA-002｜使用稳定 `stream_id` 关联资源。** 流记录、设备绑定、录像计划和录像索引通过 `stream_id` 关联；显示名称可修改，不应作为关联键。

**MEDIA-003｜设备登记不改变媒体方向。** `POST /api/streams/{stream_id}/promote` 只补充设备管理身份，不创建录像计划，也不把推流改为拉流。

## 3. 接入类型选择

| 接入类型 | 适用场景 | 媒体路径 | 专题 |
|----------|----------|----------|------|
| RTSP 拉流 | 已知摄像机或编码器 RTSP 地址 | NVR → 设备拉流 → 媒体组 | [摄像头指南](camera-guide.md) |
| ONVIF | 需要发现、能力查询、PTZ 或自动取流地址 | ONVIF 管理 + RTSP 媒体 | [ONVIF](onvif-guide.md) |
| 直播源地址 | 受支持的 RTSP、RTMP、HTTP-FLV 等远端地址 | 创建 pull 类型流 | 流管理/API |
| 编码器推流 | OBS、编码器或设备主动连接 NVR | RTMP / SRT / WHIP 等 → 媒体组 | 本文第 5 节 |
| GB28181 | 国标设备注册到平台 | SIP 控制 + 设备推 PS/RTP | [GB28181](gb28181-guide.md) |
| SIP VoIP | 门禁、SIP 电话、PBX 分机 | SIP 呼叫 + RTP/SRTP 桥接 | [VoIP](voip.md) |
| JT1078/JT808 | 车载终端 | JT808 控制（可选）+ JT1078 媒体 | [配置规范](configuration.md#jt1078--jt808实验性) |
| 小米 | 支持的小米设备 | 云/厂商传输 → 注入媒体组 | [小米摄像头](xiaomi-setup.md) |
| IPTV | M3U 直播频道 | 同源 HLS 代理，或服务端 HLS 拉流发布 | [IPTV](iptv.md) |
| Emby | 电影和剧集作为直播内容 | Emby HLS → NVR 实时流 | [Emby](emby.md) |
| HTTP JPEG / MJPEG | 图片或 MJPEG 设备 | 专用采集器 | [摄像头指南](camera-guide.md) |

Jellyfin、Plex 和通用本地/NAS 文件不应被描述为已具备与 Emby 相同的管理能力。流管理中的本地上传视频属于独立 `local_video` 输入路径。

## 4. 摄像头协议与编码

| `protocol` | 允许的 `encoding` | 备注 |
|------------|-------------------|------|
| `rtsp` | `h264`、`h265`、`mjpeg` | 空编码默认 `h264` |
| `http` | `jpeg` | HTTP JPEG 专用路径 |
| `onvif` | 空、`h264`、`h265` | 空表示探测；以设备实际能力为准 |
| `xiaomi` | `h264`、`h265` | 需要对应云账号凭据 |
| `gb28181` | 空、`h264`、`h265` | 编码信息不替代实际流检测 |

**MEDIA-101｜协议与编码分别校验。** 新配置使用 `protocol` + `encoding`；`rtsp_h264`、`rtsp_h265`、`rtsp_mjpeg`、`http_jpeg` 仅作为兼容旧格式解析。

**MEDIA-102｜声明编码不等于实际可解码。** 运行时应读取流详情中的 `video_codec` / `audio_codec` 和播放可用性。修改声明不会为不支持的视频自动转码。

**MEDIA-103｜音频需要端到端支持。** H.264/H.265 的 RTSP、ONVIF、小米设备默认允许受支持音频录像，但源音频编码、协议封装和浏览器解码必须同时满足。JPEG/MJPEG 不支持音频录像。

## 5. 推流接入

**MEDIA-201｜先创建身份再发布。** 推流槽位可在尚无发布者时存在，用于提前获得接入 URL。发布者仍须通过协议认证和封禁检查。

| 协议 | 默认端口 | 默认开关 | 凭据规则 |
|------|----------|----------|----------|
| RTMP | `11935/tcp` | 关闭 | `publish_auth_enabled` 默认 true；按流使用 `stream_key` |
| SRT | `19000/udp` | 关闭 | 按流令牌通过 SRT `streamid` 的 `u` 字段传递 |
| WHIP | `12090/tcp` + `4888/udp,tcp` | 开启 | 使用配置的按流发布令牌；WHEP 播放不受 WHIP 开关控制 |
| RTSP 发布 | `15544` | 由媒体引擎决定 | 默认应用共享发布令牌策略，另有原生 Basic/Digest 配置 |

准确 URL、别名和令牌以流记录的 `ingest_urls` 为准。RTMP 匿名发布只有显式设置 `rtmp.publish_auth_enabled: false` 才允许；RTSP 匿名发布另有 `media.rtsp_publish_auth_enabled`，两者不得混淆。

## 6. 播放协议与发现

| 客户端/协议 | 常用入口 | 默认监听/代理 | 约束 |
|-------------|----------|---------------|------|
| WebCodecs over WebSocket | `/api/.../stream/ws` | 经 `9090` | 浏览器须支持相应 WebCodecs 能力 |
| WebRTC / WHEP | API 创建会话或返回的 WHEP URL | `9090` 代理或 `12090` + ICE | 远程客户端须能完成 ICE |
| HTTP-FLV / WS-FLV | API 代理或 `play_urls` | `9090` 或媒体 HTTP | 浏览器通常经 mpegts.js 等播放器 |
| fMP4 | `/api/.../stream.m4s` 或媒体 URL | `9090` / `12090` | 使用 MSE，编码受浏览器限制 |
| HLS-TS | `play_urls` 中的 HLS | `18080` 或 API 代理 | `hls.enabled` 默认 false |
| LL-HLS / fMP4 HLS | `play_urls` 中的 LL-HLS | `12090` 或 API 代理 | 受 HLS 开关和按需切片控制 |
| RTSP / RTMP | 返回的原生播放 URL | `15544` / `11935` | 面向 VLC、FFmpeg 等外部客户端 |

**MEDIA-301｜不得硬编码播放 URL。** 集成方应读取流详情的 `play_urls`，按 `protocol` 选择其支持的地址。不同模式、应用名、国标回放和转码派生流可使用不同路径。

**MEDIA-302｜HLS 与录像独立。** 关闭 HLS 或 HLS 按需切片空闲停止不应被解释为停止录像。HLS 是播放输出，录像计划是独立写盘策略。

**MEDIA-303｜子码流只解决来源质量。** 子码流用于降低预览带宽，不会自动改变主码流录像或替代实时转码。

## 7. IPTV 与 Emby 生命周期

### 7.1 IPTV

1. 导入 M3U URL 或文本形成检测草稿；此动作不应立即把所有频道发布为流。
2. 探测并纳管选中频道。
3. 仅浏览器播放时使用 NVR 同源 HLS 代理。
4. 启用 `publish_enabled` 或存在需要拉流的录像计划时，服务端才把频道拉入媒体组。
5. 发布和录像应共享服务端 HLS 拉流，并受 `iptv.max_concurrent_pulls` 限制。

### 7.2 Emby

1. 保存 Emby 服务地址、用户 ID 和访问令牌，并验证连接。
2. 将电影或剧集纳管为媒体项。
3. 启用发布后创建 NVR 流；正常片尾默认使流下线。
4. `loop: true` 时重新创建播放会话并从头循环。

IPTV 发布和 Emby 发布要求 `media.mode: embedded`。源站编码、网络访问和第三方服务限制仍适用。

## 8. 流状态与验收

流列表响应包含 `streams`、`total`、`limit`、`offset`；单项流至少应关注：

- `stream_id`、`app_name`、`active`；
- `managed`、`management_type`、`camera_id`、`source_type`；
- `video_codec`、`audio_codec`、`in_fps`、`last_frame_time`；
- `publisher`、`subscribers`、`play_urls`、`ingest_urls`。

**MEDIA-401｜最小接入验收。** 一路来源只有同时通过以下检查才可视为完成接入：

1. 来源配置已保存且设备/频道处于预期启用状态。
2. 对应 `stream_id` 在线，实际编码符合目标客户端能力。
3. 至少一种返回的播放地址能从目标网络实际播放。
4. 如需录像，另有该 `stream_id` 的有效计划，并验证已关闭录像可回放。
5. 如需远程设备推流，已验证信令地址、媒体地址、端口和认证，而非只验证 NVR 本机监听。
