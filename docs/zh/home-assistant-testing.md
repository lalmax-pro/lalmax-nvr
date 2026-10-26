# Home Assistant 对接测试记录

跟踪：[Issue #46](https://github.com/lalmax-pro/lalmax-nvr/issues/46)。本文不包含账号密码、播放 token 或测试配置文件；最终展示截图见接入文档。

## 阶段一：视频流发布与 HA 摄像头集成

测试日期：2026-10-22。基线：`5ed0ac1`，隔离的本地 NVR 和 Home Assistant 2026.10.2 / Python 3.14.2。测试使用两路 FFmpeg 合成 H.264 + AAC 视频，通过 RTMP 推入 NVR；HA 到 NVR 的 RTSP 同时开启 Digest 鉴权和逐流播放 token。

| 场景 | 实际结果 |
|---|---|
| HA 配置向导、HTTP 鉴权 | 成功建立集成；错误密码被拒绝 |
| 稳定 NVR UUID | 同一实例经不同 HTTP 主机名添加时拒绝重复建立 |
| JPEG 缩略图 | 从 HA camera proxy 实际取得 JPEG |
| 实时视频与 AAC 音频 | HA 生成 HLS master/media playlist，声明 AVC/AAC，实际取得约 1 MB 媒体分片 |
| 动态新增发布 | 第二路自动生成摄像头实体，缩略图和视频均可读取 |
| 改名 | HA 设备及实体显示名称更新，实体 ID 不变 |
| 撤销发布 | 实体不可用，NVR 对该流的集成快照接口返回 404 |
| 重新发布 | 原实体恢复，不产生重复实体 |
| 断流 / 恢复 | 停止并重启合成视频发布进程，实体同步离线并恢复 |
| 总开关 | 停用后目录为空、实体不可用，启用后恢复 |
| 权限 | 匿名目录请求 401；普通用户可读，发布写请求 403 |
| 地址及凭据更新 | HA 重新配置切换地址和只读用户，RTSP 独立凭据仍可播放 |
| 真实密码轮换与重新认证 | 修改 NVR 测试用户密码后自动触发 HA 重新认证；更新凭据后恢复 |
| NVR 重启 | UUID、发布选择、实体身份及视频播放均恢复 |
| HA 重启 | 摄像头与视频恢复，原流实体数仍为 1 |
| 大目录 | 实际发布 107 个条目，其中新增 105 路离线流全部进入 HA，未被 100 路分页截断 |
| 清理撤销的设备 | 在 HA 设备注册表中实际清理撤销的测试设备 |
| 开启 HA 预加载后撤销 | 原 HLS 地址失效，NVR 中对应 RTSP 订阅清理 |
| NVR 不提供 FFmpeg | 限制测试 NVR 的 PATH 后，快照能力标记为 false，NVR 快照返回 503；HA Stream 能实际生成 JPEG |
| NVR 界面 | 实际登录，确认设置中的独立 HA 配置、流选择和安装说明可见 |

无 NVR FFmpeg 的回退验证中，重启后的旧 RTSP 连接在首次快照请求时发生暂时超时；连接恢复后重新请求成功。该回退依赖 HA Stream 自身的重连与取帧过程，不保证刚重启时首个请求即成功。

自动化验证：Go 新增测试覆盖持久化、备份恢复、发布生命周期、权限、参数校验、应用名和播放 token，以及大于 100 路的完整目录。HA 客户端测试覆盖真实 HTTP 响应、认证失败、重定向拒绝、完整目录、无可用播放地址和快照端点。Svelte 测试覆盖分页加载、搜索、批量选择及保存请求。

本地回归时，默认并行度的整个 API 包 race 测试达到默认 10 分钟超时；完整 storage race 测试通过。相关 API 测试以 `GOMAXPROCS=4`、`-parallel 4` 重新验证通过。前端全量测试中 380 项通过、一个原有设置测试在资源竞争下超时，单独复跑该文件的 3 项全部通过。`go vet ./...`、前端生产构建及 Python 检查通过。

`tests/home_assistant/live_smoke.py` 保存核心真实联调流程，可针对预先准备的隔离运行目录执行。该目录中的 `credentials.json`、`ha-token.json`、配置、数据库和 PID 文件属于私有测试状态，不应提交。脚本会重启目录中的测试 NVR / HA 并操作合成流，不能对生产环境运行。

## 后续阶段

事件联动、录像、云台、低延迟播放、编码适配与 HACS 分发分别补充实现、真实联调结果及 PR。转码为可选能力，仅在部署环境具备 FFmpeg 时考虑，不要求普通部署额外安装。HACS 官方目录收录属于外部审核，不把本地验证误记为官方收录完成。

## 阶段二：事件与自动化

在同一隔离 HA 2026.10.2 中启用 AI webhook，并安装事件平台和检测二进制传感器。实际通过 NVR 的 AI webhook 提交人员、车辆及移动标签，经 NVR 事件归档和发布范围过滤，SSE 推送到 HA 原生事件实体。HA 自动化监听 `lalmax_nvr_event` 后实际将测试 `input_boolean` 切换为 on。

实测通过：事件数据不包含任意元数据；匹配的人员、车辆、移动传感器打开，未匹配的保持关闭；最后检测 60 秒后自动复位；未发布流不产生 HA 事件；撤销发布后实体不可用且不再通知；重新发布及集成重载恢复通知，没有重复任务或重复事件。Go race 测试覆盖绑定映射、运行中撤销、禁用、过滤及敏感字段排除；Python 共 17 项通过，包含 SSE 分块/心跳/坏数据、实例及目录边界、去重和传感器定时清理。

`tests/home_assistant/live_events.py --runtime <隔离运行目录>` 保存核心事件实测。此脚本需要已启用 AI webhook、已发布 `ha-test-main`、HA 集成和测试状态文件，并会操作发布状态；仅在隔离环境运行。SSE 是实时通知，中断期间不补历史事件。

额外实测：实际停止并重启 NVR，事件实体在断线时标记不可用，SSE 退避重连后恢复通知，人员事件再次到达 HA。首阶段 PR #47 的完整 GitHub CI 已通过并合并，包含全量 Go race 与六个平台构建。

## 阶段三：录像浏览与回放

在隔离 NVR 为两路独立合成推流启用持续录像计划，生成真实 MP4；HA 原生媒体浏览器能按实例、发布流和片段浏览、解析临时签名播放地址。实际取回的录像可解码 H.264 视频与 AAC 音频，并完成 10 秒跳转。HA 代理的 Range 返回 206 和正确的 Content-Range，HEAD 返回文件长度，未签名匿名访问返回 401。

实测取消发布后旧签名地址的新请求立即返回 404，重新发布恢复；其他流的录像 ID 无法跨流读取；通过 NVR 删除片段后旧 HA 地址失效。使用 105 个指向有效 MP4 内容的独立测试条目验证原生 HA 的 100 条分页及后页，无重叠；测试条目已通过有数量上限的批量 API 清理，合成流录像计划已停用，保留真实录像用于后续展示。

联调发现并修正：HA 2026.10.2 使用 `register_view` 注册视图；独立流录像任务原默认关闭音频，现默认保留音频并继续尊重摄像头的显式选择。新增 AAC 文件测试暴露了音轨 ID 发布的并发竞争，现将音轨与 muxer 一并在锁内发布，H.264/H.265 同步修正。音频默认与帧源 AAC 回归测试连续 20 轮 race 检查通过。

自动化：19 项 Python 测试；相关 API、recorder、storage 的 race 回归；ruff、go vet、OpenAPI 解析均通过。`tests/home_assistant/live_recordings.py --runtime <隔离运行目录>` 保存录像到 HA 的真实联调流程，需要 PyAV、有效测试 token 和准备好的合成推流，会操作测试录像计划和发布状态。


## 阶段四：云台

真实 HA 2026.10.2 配合本地 ONVIF SOAP 模拟设备，验证连续移动与自动停止、相对和绝对位置、停止、读取预置位与跳转。验证七个原生 HA 按钮均发送实际 SOAP 命令，使用摄像头配置的 profile token。普通 NVR 账号被拒绝控制，不误触发 HA 重新认证；改用具备操作权限的账号后成功。移动过程中取消发布或主动断开请求，已授权动作的清理停止仍发出，新动作被拒绝。

另启用隔离 NVR 的回环 GB28181 服务，由 SIP 模拟设备通过真实 Digest 挑战完成注册。HA 经 NVR 发出的左右、上下、变焦六方向及自动停止均实际到达 UDP SIP 设备；校验通道 ID、8 字节命令、方向位及校验码。GB28181 的多轴、绝对位置等不支持操作明确返回错误。

上述为协议模拟设备联调，不代表实体云台已验证。网络故障时停止也可能失败，接口会返回失败，不能承诺硬件一定停止。`tests/home_assistant/onvif_fixture.py`、`gb28181_fixture.py` 为模拟端；`live_ptz.py` 和 `live_gb_ptz.py` 保存隔离环境流程。HA 原生按钮及控制动作已提供中英文名称。20 项 Python 测试、Home Assistant API 专项 Go race、ruff、go vet、OpenAPI 解析通过。

补充：PR #49 在获准不等待主构建而合并后，完整 CI 随后全部通过，包含全量 Go race、前端及六个平台构建。


## 阶段五：可选 WebRTC 低延迟

隔离 HA 开启低延迟选项后，使用 HA 原生 `camera/webrtc/offer` WebSocket 接口协商，Pion 接收端实际收到 H.264 RTP 视频包并建立连接。特意去掉初始 offer 的全部候选地址，仍能通过完整 answer 建立 LAN 连接；不依赖浏览器预先完成 ICE 收集。同一摄像头继续提供 `camera/stream` HLS。当前 AAC 源在 WebRTC 中为视频，声音继续使用 HLS。

实际验证播放器退订、撤回发布和 HA 集成重载均关闭媒体端会话，接收端停止收包并进入 closed 状态。撤回后新协商被拒绝，原令牌仍可用于清理；Go race 验证令牌篡改、串流、过期及跨来源 Location 拒绝，正确使用真实 app 与播放 token。Python 验证分块 SDP 完整读取、体积限制、禁止重定向及 pending 协商关闭清理。测试接收端位于 `tests/home_assistant/webrtc_peer`，仅监听回环地址，不接收账号凭据。未验证 TURN/公网穿透。


## 阶段六：H.265 兼容

用本机已有 FFmpeg 生成 Enhanced RTMP 的 H.265/AAC 合成源，实际进入 NVR，再由 HA 通过鉴权 RTSP 读取。HA 快照为有效 JPEG；HLS master 宣告 `hvc1` 和 AAC，取回 init 与真实分片后可解码 HEVC 视频和 AAC 音频。当时 HEVC 流尚未宣告 WebRTC 能力；2026-10-23 的补充验证已解除此限制，见下文。未启动自动转码，也不要求用户安装 FFmpeg；测试源生成使用测试机既有工具。

中途启动录像时发现 CRA 源无片段：原 H.265 录像器只等待 IDR，错过初始 IDR 后无限等待。已增加 BLA/IDR/CRA 随机访问点处理，开始新片段时丢弃可能引用片段外帧的 leading RASL，直到 trailing pictures。实际再录制，HA 原生媒体解析出的 MP4 可解码 HEVC 与 AAC，并完成 10 秒 seek。回归包含 CRA 开始与旋转、首段 RASL 排除和 Enhanced RTMP IRAP 标记，相关 recorder/media/nalutil race 检查通过。

这验证媒体服务与 HA 链路保留原始编码，浏览器仍需具备 HEVC 解码能力。`tests/home_assistant/live_codecs.py --runtime <隔离目录>` 保存验证，需已准备 HEVC/AAC 流与测试录像计划，结束后恢复原计划状态。


## 阶段七：安装包与 HACS 分发评估

从官方 Release 获取 HACS 2.0.5，在隔离 Python/HA 环境运行其真实集成仓库校验与 GitHub 下载路径，取得主仓库 main 的完整集成。显式选择 main 才能避开不包含集成的旧 NVR Release；默认最新 Release 的首次安装尚不可用，不能记为 HACS UI 首次安装通过。独立 HA 分发仓库可以隔离两者版本，仓库选择待确认；官方目录和 Brands 审核仍是外部事项。

打包脚本只收集集成公开源码、翻译和 MIT 许可证，排除缓存与测试状态，ZIP 根目录直接为 `manifest.json`。实际生成 0.6.0 ZIP，清除隔离 HA 的旧集成目录，从 ZIP 安装后重启，原摄像头恢复，配置与实体身份保留。HA 专项 CI 在测试后生成同一 ZIP，后续 NVR tag Release 会附带此包。支持导出 HA-only 源码目录，但脚本不创建或发布仓库。

## 2026-10-23 补充：HEVC WebRTC、TURN 与设备授权

此前 HA 接口额外限制了 H.264，底层 lalmax 的 H.265 WebRTC 实现已经存在。本次解除该限制，目录同时宣告 H.264/H.265 能力，并在调用媒体引擎前校验 offer 中的视频编码，拒绝只有 H.264 的 HEVC 请求、无效 SDP、被禁用的视频 m-line 和不接收视频的方向。不会启动转码。

隔离环境仍为 HA 2026.10.2、集成 0.6.1，以及两路 640×360/15fps 的合成 H.264/AAC、H.265/AAC 输入。真实 HA WebSocket 协商后，Pion 接收端取得 H.265 RTP，按 RFC 7798 解包，再由 PyAV 实际解出至少三帧 640×360 视频。HA 浏览器中的两路 `ha-web-rtc-player` 均持续播放，HEVC 视频尺寸为 640×360、播放时间持续增长。H.264 原路径也完成实际收包与解码回归。

使用 `tests/home_assistant/turn_server` 启动真实的认证 TURN/UDP 测试服务，通过 HA `web_rtc.ice_servers` 配置传给接收端。接收端强制 `ICETransportPolicyRelay`，初始 offer 不包含候选地址，连接后检查选中候选类型确实为 `relay`，H.264/H.265 的 RTP 与实际解码均通过。H.265 关闭播放器、撤销发布及重载集成的会话清理通过；HLS 仍可单独请求。随后再次验证 HEVC JPEG、HA HLS 的 HEVC/AAC 实际解码，以及 HA 录像的 HEVC/AAC 解码和 10 秒 seek，均通过。TURN 控制端只监听回环地址，权限仅允许测试媒体主机，测试凭据存于临时私有文件。

该验证覆盖受控环境中的真实 TURN 协议和中继媒体，没有覆盖真实公网、双端 NAT、不同运营商网络或 TURN/TCP/TLS。实体云台仍需真实设备；现有 ONVIF/GB28181 验证使用协议模拟设备。

复现时先准备原有隔离 HA/NVR 与两路合成输入，并启动接收端。普通部署不需要运行这些测试程序：

```sh
go run ./tests/home_assistant/webrtc_peer
python tests/home_assistant/live_webrtc.py --runtime <隔离目录> --codec h265 --mode close
```

TURN 测试需要私有 JSON 文件，包含 `username`、`credential` 和 `relay_address`（本机拥有的、引擎公布的局域网 IPv4 地址）。将相同凭据配置到隔离 HA 的 `web_rtc.ice_servers`，URL 为 `turn:127.0.0.1:13478?transport=udp`，重启测试 HA 后运行：

```sh
go run ./tests/home_assistant/turn_server --config <私有凭据文件>
python tests/home_assistant/live_webrtc.py --runtime <隔离目录> --codec h265 --mode close --relay
python tests/home_assistant/live_webrtc.py --runtime <隔离目录> --codec h265 --mode revoke --relay
python tests/home_assistant/live_webrtc.py --runtime <隔离目录> --codec h265 --mode reload --relay
python tests/home_assistant/live_webrtc.py --runtime <隔离目录> --codec h264 --mode close --relay
```

这些脚本共享同一测试接收端，并会改变发布和集成状态，必须依次执行。能力断言使用 HA 的 `camera/capabilities` WebSocket 接口，不能从 camera state attributes 推断 `frontend_stream_types`。

另建没有 GitHub 凭据的隔离 HA，运行官方 HACS 2.0.5 配置流程，由该实例生成设备代码，在 GitHub 完成交互式设备授权。授权页面明确只申请公开资料，不申请私有仓库权限。HACS 成功创建配置项并进入 `running`，`disabled_reason` 为空，实际加载 5009 个仓库条目。此前正式版首次安装和 RC → V1.0.2 升级测试保持有效；此次补齐了先前未覆盖的真实设备授权流程。HACS 官方目录/Brands 外部审核仍未完成。

## 2026-10-23 补充：真实 ONVIF 设备，经 NVR 接入 HA

从本地已有配置只读获取一台真实海康 ONVIF 摄像机的接入信息，在隔离 NVR 中建立测试设备并发布其真实视频；未修改原本地数据库、摄像机编码或 GB28181 注册配置，录像计划保持关闭。HA 始终只保存 NVR 的 API / RTSP 访问凭据，不直接连接原始摄像机。设备账号只在 NVR 使用，真实画面未保存或提交到仓库。

实测真实设备 → NVR → HA 的 JPEG 解码为 1920×1080，HLS 实际解码 H.264 与 AAC；HA WebRTC 协商后接收端实际收到并解码三帧 1920×1080 H.264 视频，关闭会话通过。HA 真机预览实际使用 `ha-web-rtc-player`，尺寸 1920×1080、播放时间持续增长；首次切换出现过短暂协商错误，随后自动恢复播放。复用原接收端，可用以下参数验证已发布的其他测试设备：

```sh
python tests/home_assistant/live_webrtc.py --runtime <隔离目录> \
  --stream-id <NVR发布流ID> --entity <HA摄像头实体ID> \
  --codec h264 --width 1920 --height 1080 --mode close
```

云台方面，经 HA 调用的读取预置位、0.1 秒连续变焦与自动停止、原生停止按钮，均得到 NVR 和真实设备成功响应。设备报告的坐标始终为零，不能据此确认镜头发生物理位移。进一步读取真实 ONVIF 配置：只声明连续速度空间，未声明绝对位置空间；`AbsoluteMove` 被设备返回 401（SOAP 客户端最终报告 `password text auth failed (401)`），相同凭据的其他已测命令成功。绝对位置不标记通过，也不把失败误记为 HA 的 NVR 登录失效。已补发停止，设备报告 `moving=false`。该设备不能代替具备完整云台能力的真机验收。

真实 GB28181 注册及控制尚未验证；本次没有修改摄像机已有 GB 配置，现有 GB 验证仍是实际 SIP/Digest 协议模拟端。真实公网环境暂不可用，按维护者决定暂缓；本地强制 TURN/UDP 中继的结果保持有效。

## 2026-10-26：HACS 默认目录提交准备

集成 0.6.2 复用现有项目 Logo，在 `brand/icon.png` 中随集成提供，不再依赖单独 Brands 仓库收录。打包器只增加对八个标准品牌 PNG 文件名的支持，不收集任意截图；新增回归验证品牌资源入包、无关图片排除和 ZIP 可复现性。

运行官方 `ghcr.io/home-assistant/hassfest` 后补齐 `http` 依赖，并将事件类型翻译调整为 `state_attributes.event_type.state`；校验最终报告 1 个集成、0 个无效集成。添加官方 HACS Action 与 Hassfest 工作流，不设置 `ignore`，也不关闭任何检查。中英文低延迟选项同步说明 H.264/H.265。

从新 ZIP 安装到隔离 HA 2026.10.2 并重启，确认 `icon.png`、`logo.png`、`icon@2x.png` 端点均返回本地 PNG（后两者使用标准回退），与项目 Logo 字节一致；英语、中文事件类型翻译实际返回 Motion / 移动检测，原 36 个实体身份保持不变。23 项 Python 测试、Ruff、actionlint 和 diff 检查通过。满足官方校验及新 Release 要求后，再以个人账号向 `hacs/default` 提交，仅增加一条仓库记录；提交不等于收录。
