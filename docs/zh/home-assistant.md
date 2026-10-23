# Home Assistant

lalmax-nvr 可以把选中的流发布为 Home Assistant 摄像头，支持实时视频、缩略图和在线状态。HA 从 NVR 的 RTSP 服务取流，复用 NVR 已有接入，不会重新连接原始摄像头，也不会因发布而开始录像。

## 安装与配置

1. 将本仓库 `custom_components/lalmax_nvr` 完整复制到 HA 配置目录的 `custom_components/lalmax_nvr`。该目录中应直接包含 `manifest.json`、`__init__.py` 等文件。重启 HA。要求 Home Assistant **2026.10.2 或更新版本**。也可将 CI / Release 的 `lalmax_nvr.zip` 直接解压到该目录，ZIP 根部就是 `manifest.json`。
2. 在 NVR 的 **设置 → Home Assistant** 中启用发布，选择流并保存。可以搜索后批量选择，也可以在流管理卡片上逐路选择。未登记为摄像头的推流也可以发布。
3. 必要时设置 RTSP 服务地址，例如 `rtsp://192.168.1.20:15544`。不要包含用户名、密码或 `/live/流名`。留空使用媒体引擎对外地址；默认的回环地址由 HA 集成替换为 NVR HTTP 地址的主机名。如果媒体引擎与 NVR 不在同一主机，请显式配置 HA 能访问的媒体引擎地址。
4. HA 中进入 **设置 → 设备与服务 → 添加集成 → lalmax-nvr**，填写 NVR HTTP 地址（例如 `http://192.168.1.20:9090`）和 NVR 账号。建议给 HA 单独创建普通用户，只允许读取。
5. 如果开启了 `media.rtsp_auth_enable`，单独填写 RTSP 用户名和密码。这不是 NVR HTTP 账号，不能相互替代。RTSP 使用 TCP，兼容 Basic / Digest 服务端鉴权。
6. 发布流会生成 `camera` 实体。可将实体加入 HA 仪表盘，例如：

```yaml
type: picture-entity
entity: camera.front_door
camera_view: live
```

这是 HA 自定义集成；安装包与 HACS 分发说明见下文，尚未纳入官方目录。

## 同步与撤销

目录每 30 秒同步一次，单次返回全部已发布流，不受普通流列表每页 100 路限制。新增发布会自动建立实体；断流保留实体并标记不可用，恢复后继续使用原实体。实例 UUID 与发布选择保存在数据库中，改名、NVR / HA 重启及集成重载不会改变身份。备份数据库会同时备份这些配置；同时运行复制出的两个 NVR 时需要使用不同实例身份，不能把同一数据库副本当成两个独立实例接入同一 HA。

取消发布或关闭总开关后，集成在下一次同步时将实体标记不可用，并停止对应 HA 视频工作器。实体注册记录保留，可在 HA 中手动清理。重新发布同一流会恢复原实体。源流删除后，保留的发布条目会离线，需在发布选择中取消；该行为允许临时推流断开后使用相同 ID 恢复。

撤销集成入口不会使其他客户端持有的 RTSP 凭据失效；普通用户也仍受现有 NVR 账号权限约束。需要严格的流级隔离时，应同时配置媒体访问策略。

## 鉴权与播放 token

HTTP API 始终使用 NVR Basic 账号鉴权，写发布配置要求超级管理员。目录不返回上游地址、摄像头密码或 RTSP 账号密码。

如果设置了 `auth.playback_tokens`，每路发布流必须有对应 token；集成目录的 RTSP URL 会附带该流的播放 token，因此目录本身应按媒体凭据保护，不应分享或写入公开日志。没有对应 token 的条目标记为 `rtsp_unavailable`，不影响其他流同步。发布不会关闭或绕过已有鉴权。

## 快照与编码

优先验证 H.264 视频和 AAC 音频。已实际验证 H.265/HEVC + AAC 从 RTSP 经 HA 生成快照、HLS 和读取 MP4 录像，保留原始编码。HLS 声明 `hvc1`；终端仍需原生 HEVC 解码支持，不能保证所有浏览器播放。如需更广的终端兼容性，可发布设备的 H.264 子码流。当前不自动转码。

NVR 按流抽帧快照使用 PATH 中的 FFmpeg，并有 10 秒缓存。NVR 未安装 FFmpeg 时，集成会改由 HA 的 Stream 功能从 RTSP 生成静态图。仅有流管理绑定关系的摄像头快照不会被误用为其他流的缩略图。

## 排障与接口

- **无法连接**：检查 HA 到 NVR HTTP 端口是否可达，以及 NVR 用户是否启用。反向代理必须转发 `/api/integrations/home-assistant`。
- **配置成功但不可用**：确认已启用发布、选中该流、流在线，且播放 token 已配置。IPTV 等只有进入 lalmax 引擎后才能复用 RTSP。
- **缩略图正常但视频失败**：分别检查 HA 到 RTSP 端口的连通性、RTSP 账号、流 token 和编码；HTTP 登录成功不代表 RTSP 鉴权成功。
- **快照失败**：检查 NVR FFmpeg 及媒体引擎地址是否可达；快照端点返回 503 表示依赖缺失或当前取帧失败。NVR 无 FFmpeg 时 HA 应使用 Stream 静态图。
- **地址或密码变化**：在 HA 集成菜单选择重新配置；HTTP 凭据失效会触发重新认证。更换成另一个 NVR 实例会被拒绝，避免把旧实体错误绑定到新系统。

接口均需 NVR HTTP 鉴权：

| 方法 | 路径 | 用途 |
|---|---|---|
| GET / PUT | `/api/settings/home-assistant` | 发布设置；PUT 为完整替换，要求超级管理员 |
| PUT | `/api/integrations/home-assistant/streams/{stream_id}` | 单路选择，JSON 为 `{"enabled":true}` 或 `false`，要求超级管理员 |
| GET | `/api/integrations/home-assistant` | 已发布流目录、实例 UUID、快照能力 |
| GET | `/api/integrations/home-assistant/streams/{stream_id}/snapshot` | 已发布流 JPEG 快照 |

HACS 安装包与分发说明见下文。实现与联调进度见 [Issue #46](https://github.com/lalmax-pro/lalmax-nvr/issues/46)。


## 检测事件与自动化

每路发布流会增加一个原生“事件”实体，以及移动、人员、车辆检测二进制传感器。事件通过鉴权 SSE 实时推送；NVR 对每条事件重新检查发布清单和摄像头绑定，仅输出规范化类型与检测标签，不输出任意元数据、消息或文件路径。

AI webhook / 浏览器检测产生 `ai_detection`；`motion` 标签或 MQTT 的 `motion` 触发动作驱动移动传感器，人员及车辆标签驱动对应传感器。集成消费 NVR 已有检测结果，不负责推理，请先配置检测器或 MQTT 输入。传感器在最后一次匹配检测后 60 秒复位；断开事件连接或取消发布时清空。它表示最近检测到目标，不代表持续占用。

可用 HA 原生事件实体触发器，也可监听 `lalmax_nvr_event`：

```yaml
triggers:
  - trigger: event
    event_type: lalmax_nvr_event
    event_data:
      stream_id: front-door
      type: ai_detection
conditions:
  - condition: template
    value_template: "{{ 'person' in trigger.event.data.labels }}"
actions:
  - action: light.turn_on
    target:
      entity_id: light.entrance
```

字段包括 `instance_id`、`config_entry_id`、`stream_id`、`id`、`type`、`source`、`started_at`、`labels`。多台 NVR 存在同名流时应同时按实例或配置项筛选。支持类型：`ai_detection`、`motion`、`mqtt_trigger`、`recorder_reconnected`、`health`。连接中断时事件实体不可用，并自动退避重连；这是去重的实时通知，中断期间的历史事件不补发，可在 NVR 事件时间线查看历史。反向代理需对 `/api/integrations/home-assistant/events` 关闭缓冲并允许长连接。


## 媒体浏览器中的录像

HA 中进入 **媒体 → lalmax-nvr recordings → NVR 实例 → 发布流**。录像按时间从新到旧排列，每页 100 条。仅提供已完成的 MP4；需要先在 NVR 配置启用的录像计划。单纯发布流不会开始录像，离线流仍可查看已有录像。

HA 使用临时签名地址代理播放，地址不包含 NVR 账号密码和文件路径。代理分块传输，支持 Range 和 HEAD，便于拖动进度。每次读取都在 NVR 重新检查发布状态和实际录制流归属；取消发布后，旧签名地址的新读取立即被拒绝，已经传输中的请求可能继续完成。删除或归档录像后不可播放。旧数据缺少流 ID 时，仅在摄像头 ID 等于发布流 ID 时兼容匹配。

已验证 H.264/AAC MP4。H.265 MP4 依赖播放终端支持；MJPEG 目录和裸流录像不作为视频片段提供。此功能只浏览已有录像，不自动转码。自动化可以使用媒体浏览器选择出的 media-source 片段标识播放录像。


## 云台控制

发布流绑定到 ONVIF 或 GB28181 摄像头后，会生成左、右、上、下、放大、缩小和停止七个 HA 原生按钮。移动要求 NVR HTTP 账号具备超级管理员权限；普通账号仍可读取视频、事件和录像。需要控制时在 HA 中重新配置账号。控制权限不足会返回动作错误，不会误触发重新认证。

动作 `lalmax_nvr.ptz` 以摄像头实体为目标，支持 `mode`（`continuous`、`relative`、`absolute`、`stop`、`goto_preset`）、归一化的 `pan` / `tilt` / `zoom`、可选 `preset_token`，以及 0.1–5 秒的 `duration`（默认 0.5 秒）。绝对位置的 zoom 范围为 0–1。`lalmax_nvr.ptz_presets` 通过动作响应返回 ONVIF 预置位。GB28181 只接受单轴连续移动及停止，不接受相对、绝对或预置位动作。

连续移动的自动停止由 NVR 执行；请求断开或移动中取消发布时，仍会对已确定的设备尝试停止。移动或停止失败会返回错误，网络或设备故障可能阻止物理停止。新命令都会重新检查发布状态和当前绑定。协议支持不代表每台 ONVIF 摄像头都有云台能力。本次使用真实 HA 对接本地 ONVIF SOAP 和具备 Digest 注册鉴权的 GB28181 SIP 模拟设备，尚未验证实体云台硬件。


## 可选 WebRTC 低延迟播放

在 HA 集成的“重新配置”中启用低延迟选项。在线 H.264 发布流会同时提供 WebRTC 和 HLS，由 HA 播放器选择路径。默认关闭，可随时重新配置关闭。AAC 音频不能通过当前 WebRTC 路径播放，需要声音时使用 HLS；不会启动转码。

HA 通过 NVR 鉴权接口完成 WHEP 协商，不直接访问媒体引擎的 HTTP 地址。媒体通过 ICE 在终端与引擎之间传输，请确保终端可访问引擎公布的 UDP 地址与端口；只验证了局域网直连，未提供 TURN 中继或公网 NAT 穿透保证。引擎返回完整 ICE answer，LAN 已验证初始 offer 无 candidate 时也能连接。

关闭播放器、卸载集成或撤回发布会清理会话。撤回发布后，已经签发的清理令牌只能删除原会话，不能再创建播放。令牌绑定流、实例及引擎来源，24 小时过期，NVR 重启后失效；异常退出时还依赖引擎的超时清理。


## 最终展示

全部集成及发版测试完成后截取，使用 NVR V1.0.2、HA 集成 0.6.1 与隔离 HA 2026.10.2。视频是合成测试输入，云台通过 ONVIF/GB28181 协议测试端验证。以下均为实际应用截图。

![HA 中的 H.264 与 H.265 双路预览](../images/home-assistant/live.png)

[事件联动与云台](../images/home-assistant/events-ptz.png) · [HEVC 录像播放](../images/home-assistant/recording.png) · [NVR 发布设置](../images/home-assistant/publication.png)

发版验证：[完整构建及 7 个附件](https://github.com/lalmax-pro/lalmax-nvr/actions/runs/39884373628)、[重复发布事件跳过构建](https://github.com/lalmax-pro/lalmax-nvr/actions/runs/39884376124)、[三种 Docker 架构的正式二进制与 FFmpeg 运行](https://github.com/lalmax-pro/lalmax-nvr/actions/runs/39886532867)。GHCR 包当前为私有，匿名拉取需管理员设置公开可见性；HACS 安装和 Release ZIP 下载不受该设置影响。

## HACS 安装包与分发

仓库符合 HACS 单集成目录结构，提供 `hacs.json`，声明已验证的最低 HA 2026.10.2。集成版本在 `manifest.json` 中独立维护，不与 NVR 二进制版本号绑定。HACS 官方目录收录及 Home Assistant Brands 需要外部审核，尚未完成。

HA 专项 CI 在检查通过后生成 `lalmax_nvr.zip` 构建产物；NVR tag 发版也会附带此包。手动打包：

```sh
python scripts/package_home_assistant.py --output /tmp/lalmax_nvr.zip
```

如采用独立 HA 分发仓库，可增加 `--export-dir /tmp/lalmax-nvr-home-assistant`（新目录），导出集成、HACS 元数据、安装说明与许可证，不包含测试状态或 NVR 二进制，不会自动创建或发布仓库。这样可以避免 NVR 版本号干扰 HA 更新。

继续使用本主仓库分发。在 HACS 中打开 **⋮ → Custom repositories**，添加 `https://github.com/lalmax-pro/lalmax-nvr`，类型选择 **Integration**。打开仓库并下载最新稳定版，重启 HA 后在“设置 → 设备与服务”中添加 **lalmax-nvr**。使用 **V1.0.1 或更新版本**；V1.0.0 尚未包含本集成。

已在 HACS 2.0.5 / HA 2026.10.2 中实际通过界面首次安装 **V1.0.2**；另一套隔离 HA 从 **v1.0.1-rc.1** 通过 HACS 升级到 **V1.0.2**，24 个实体的 entity_id、unique_id 和配置项 ID 均保留。两个标签的集成版本均为 0.6.1，此次验证的是仓库 Release 升级路径。测试预先配置 HACS 的 GitHub 身份，未覆盖交互式 GitHub 设备授权。安装后已联调实时媒体、缩略图、事件、录像及协议模拟云台。

退出测试版时，关闭仓库的 beta 开关实体；必要时在 **Redownload → Need a different version?** 中选择稳定版。下载更新后重启 HA。HACS 2.x 显示仓库 README，`info.md` 不会替代它。

参考：[HACS 集成结构](https://www.hacs.dev/docs/publish/integration/)、[仓库元数据](https://www.hacs.dev/docs/publish/start/#hacsjson)。
