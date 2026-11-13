# 通用转码部署与当前边界

关联 [Issue #66](https://github.com/lalmax-pro/lalmax-nvr/issues/66)、[架构设计](transcoding-design.md)、[验收记录](transcoding-testing.md)。

本文描述包含通用实时转码的源码版本。转码默认关闭。使用发布包时，可先检查后台是否提供「通用转码能力」及 `/api/transcoding/capabilities`。各平台与后端的验证记录见[验收记录](transcoding-testing.md)。

## 本地运行

将以下配置合并到 NVR 配置。示例使用内嵌媒体引擎，在本机通过 RTMP 推入一路测试流。部署时请替换推流密钥。已有源可以直接用于转码，RTMP 监听仍需开启，供派生流回灌。

```yaml
media:
  mode: embedded
  rtmp_listen_addr: "127.0.0.1:11935"
rtmp:
  enabled: true
  port: 11935
  stream_keys:
    local-demo: "replace-with-a-private-key"
transcoding:
  enabled: true
  max_concurrent: 2
  threads_per_job: 2
  worker_enabled: false
  ffmpeg_path: ""
  ffprobe_path: ""
  gst_launch_path: ""
  gst_inspect_path: ""
```

路径留空时从 NVR 进程的 `PATH` 查找工具。也可以填写该运行环境内的绝对路径。配置中的 `rtmp_listen_addr` 包含主机和端口，端口应与 `rtmp.port` 一致。回灌连接使用 `127.0.0.1`。如需同时接收远端 RTMP 推流，可以监听 `0.0.0.0:11935` 并保留发布鉴权。后端通过 RTSP 拉取源，因此 NVR 进程也要能访问内嵌引擎公布的 RTSP 地址。

已有二进制时可直接传入配置路径。macOS/Linux 从源码构建时，项目脚本会打包网页与后端。

```sh
./scripts/unix/build.sh
./bin/lalmax-nvr -config ./config/lalmax-nvr.yaml
```

改动工具路径、监听端口、`enabled` 或 `worker_enabled` 后重启 NVR。缺少后端工具时，NVR 仍可启动，能力页会显示检测结果。使用 macOS Homebrew GStreamer 时，如遇插件扫描或动态库加载失败，可先在测试进程中设置 `DYLD_FALLBACK_LIBRARY_PATH=/opt/homebrew/lib`，并将 `GST_REGISTRY` 指向可写的本地缓存文件。

本机没有现成流时，可用以下命令推送合成 H.264/AAC 测试流。将 URL 中的占位符替换为上面配置的密钥，然后在流详情确认 `local-demo` 在线。

```sh
ffmpeg -re -f lavfi -i testsrc2=size=1280x720:rate=25 \
  -f lavfi -i sine=frequency=997:sample_rate=44100 \
  -c:v libx264 -preset ultrafast -pix_fmt yuv420p -g 25 \
  -c:a aac -f flv 'rtmp://127.0.0.1:11935/live/local-demo?stream_key=replace-with-a-private-key'
```

## Docker 可选后端

默认 [Dockerfile](../../Dockerfile) 已安装 FFmpeg。可选 GStreamer 构建参数：

```sh
docker build --build-arg WITH_GSTREAMER=true -t lalmax-nvr:transcode-gst .
```

默认参数为 false。镜像保留 FFmpeg/ffprobe，因为快照、媒体检查与 GStreamer 源尺寸探测仍使用它们。不要挂载 Docker socket。

插件依赖包括 gstreamer-tools、base/good/bad/ugly 和 libav 包；具体目标架构可用性必须通过构建与实际检测确认。包含 x264 的构建涉及对应许可证，请在分发镜像时核对许可证要求。linux/arm64 可选镜像构建和有限编码 smoke test 已通过；其他架构和完整容器 API 媒体链路仍未验收，详见[记录](transcoding-testing.md)。

## 首次使用步骤

1. 用超级管理员账户打开「设置 → 通用转码能力」，选择 FFmpeg 或 GStreamer 并检测默认模板。
2. 在同一设置页的「转码模板与任务」创建模板，例如 H.264 720p + AAC。对**已保存的模板**执行检测。默认模板的检测结果不能用于其他媒体规格。检测缓存有效期为 10 分钟，重启后手动任务需要重新检测。
3. 填入在线源流 ID、实际应用名（示例为 `live`）和模板，先保存禁用的手动任务。启用后点击启动。等任务进入 `running` 再拉流，`starting` 表示尚未取得可播放输出。
4. 在源流详情选择派生输出预览，或用管理员接口查询 `/api/streams/{stream_id}/variants/{job_id}/play`。接口返回的 `URL` 为带临时令牌的 RTSP 地址，请按凭据处理。
5. 可用 `ffprobe -v error -rtsp_transport tcp -show_streams '<带令牌的 RTSP URL>'` 核对编解码和分辨率。测试结束后先停止派生录像、转推等消费者，再停止手动转码任务。

### API 启动顺序

| 步骤 | 请求 | 参数或结果 |
| --- | --- | --- |
| 保存模板 | `POST /api/transcoding/profiles` | 保存 `backend`、`video`、`audio`，记录响应的 `id` 和 `revision` |
| 检测模板 | `POST /api/transcoding/profiles/{id}/probe` | `can_enable: true` 后可启用任务 |
| 保存任务 | `POST /api/transcoding/jobs` | `source` 包含 `engine_id: "embedded"`、`stream_id`、`app_name`，另传 `profile_id`、`profile_revision`、`enabled`、`run_policy`，记录响应的 `generation` |
| 启动手动任务 | `POST /api/transcoding/jobs/{id}/start?generation=N` | `202` 表示已受理。查询 `GET /api/streams/{stream_id}/variants` 等待 `running` |
| 停止任务 | `POST /api/transcoding/jobs/{id}/stop?generation=N` | `204` 表示已停止该任务的运行需求 |

`manual` 由操作员显式启动。`always` 在源可用时由后台启动并在故障后退避重试。`on_demand` 由消费者获取租约后启动。网页创建的是手动任务，其他策略可通过 API 设置。常驻任务的显式停止会持久化，重启后需要显式启动才能重新运行。按需任务显式停止后可调用 `resume?generation=N` 重新接受租约。

| 状态或错误码 | 检查项 |
| --- | --- |
| `probe_required` | 对保存的模板重新检测 |
| `source_offline` | 确认原始流在线，应用名匹配，RTSP 可拉取 |
| `rtmp_disabled` | 打开 RTMP 监听并重启 |
| `capacity_exceeded` | 检查 `max_concurrent` 和现有运行任务 |
| `output_timeout` | 检查源是否持续出帧、模板是否支持输入编解码、后端能否回灌到配置的 RTMP 端口 |

## 已知限制

- 全局开启目前通过配置文件完成，网页不会自动开启。
- GStreamer 固定模板的本机和可选 linux/arm64 镜像自检已通过；运行中的 H.264/HEVC 合成发布源已经通过 API 管理的 RTSP → GStreamer → RTMP → RTSP 解码；实体摄像机和生产稳定性仍未验收。macOS Homebrew 本机自检需为进程设置 `DYLD_FALLBACK_LIBRARY_PATH=/opt/homebrew/lib`，该环境变量并未写入 NVR 全局配置。
- FFmpeg copy 模式使用内置的合成 H.264/AAC 样本执行直拷、媒体检查与解码自检；真实源仍须符合 H.264/AAC 的 copy 限制，自检不等于实际源或媒体回灌已验证。GStreamer copy 模式自检仍未支持。
- 管理员 RTSP 派生地址、同源 FLV 接口以及 HA 目录地址均已实际取回解码；第六轮还验证了 NVR 侧 HA-WHEP 的 Chromium H.264 + PCMA/8000 音视频播放、ICE/DTLS、RTP、视频解码和 Web Audio 非静音采样。真实 Home Assistant 客户端/实例仍为 `BLOCKED_ENVIRONMENT`。StreamDetail 派生预览仍走 HTTP-FLV/mpegts.js，不支持 PCMA 音频，不能把该路径当作 WHEP 验收。
- 检测有效期 10 分钟；已运行任务不会因检测过期被停止，但失败后的重新启动需要新的有效检测。
- 常驻策略 API 可配置；当前网页创建的是手动任务。持久化的已启用常驻任务在重启或检测过期后，由有界后台协调重新自检再恢复；自检失败遵循退避并记录安全错误码，运行中合成源的常驻任务实际进程重启恢复和解码已通过，实体海康源的视频 copy + 音频转码已有有限实际解码证据；其他实体源场景及长时间运行仍待验证。
- 不支持自动硬件加速、跨后端回退、任意命令、任意外部输入 URL 或离线录像转码。
- 独立 [ARM64 scratch Dockerfile](../../Dockerfile.arm64) 不包含后端工具，不应当作转码镜像使用。

## 安全验收要求

上线前必须实际验证源拉取、派生发布、RTSP 播放的鉴权；拒绝无令牌和跨源播放；验证取消 HA 发布与全局停止的不同作用域。不得以参数构造、进程启动或前端构建成功替代实际媒体解码验收。


## 派生输出与可选功能

- **按需订阅**：`POST /api/transcoding/jobs/{id}/leases?generation=N&wait_ms=20000` 可等待实际媒体就绪。租约会按需续期，最后一个租约释放或过期后自动收回媒体进程。前端派生预览已接入申请、续期和释放。
- **Home Assistant 预加载**：发布列表中选择 `on_demand` 转码 Job 后可单独勾选 `preload_transcode`；默认关闭，关闭 HA 发布、任务被禁用或代次变化后撤销租约。
- **WebRTC 有声派生输出**：选择 G.711 A-law 8kHz mono / H.264 模板，媒体检测必须成功；只接受能够协商 PCMA 的 WHEP SDP，其他音频明确拒绝，不静默回退 AAC 或原流。派生会话签名令牌已改为认证加密。
- **硬件编码**：可显式选择 `h264_nvenc`；必须完成当前机器上实际 FFmpeg 编码、ffprobe 与解码自检后才能启用，失败绝不回退 `libx264`。Intel/AMD 和其他架构不声称支持。
- **多档共享解码**：本期只完成对照评估，保留独立编码任务与同配置去重；参见 [实验记录](transcoding-multitier-evaluation.md)。未启用未通过收益与故障隔离门槛的共享解码。
- **派生录像**：通过 `/api/transcoding/jobs/{id}/record/start|stop?generation=N` 显式启停。录像按源流 ID 查询，数据库关联本次物理流、任务代次、模板修订与实际编码。本机复现了多 slice H.264 输出生成的 MP4 解码失败。同一 FFmpeg 模板改用 `threads_per_job: 1` 后通过回放复测。使用录像前请验证当前模板的实际文件。[验收记录](transcoding-testing.md#2026-11-13本机完整功能复核)。
- **派生转推**：Relay 创建接口接受可选 `transcode_job_id`；存逻辑源与 Job 而非物理流 ID，启动时从内部注册表解析受保护输出并获取定向 RTMP 播放凭据，不能自行启动转码。失败不自动回落原流。
- **独立 Worker**：启用 `worker_enabled: true` 后，NVR 启动一个临时独立的本机 Worker 来运行固定 FFmpeg/GStreamer 管线。一次性 HMAC 控制消息通过私有 stdin 传输；无网络控制端口、不经 shell、不继承 NVR 密码/数据库 DSN；父进程管道断开触发退出与进程回收，原有 Always/OnDemand 管理器负责恢复。现阶段为**同用户身份的进程级隔离**，不等同于 seccomp、容器或专用 OS 用户隔离；高安全等级环境必须补充独立运行账户、沙箱和设备访问控制。
- **历史录像离线转码**：不在实时 Issue #66 范围，见 [独立后续设计](transcoding-offline-followup.md)。
