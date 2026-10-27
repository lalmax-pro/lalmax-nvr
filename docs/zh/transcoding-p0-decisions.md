# 通用实时转码 P0 决策与支持边界

- 状态：P0 方案已整理并自审，本设计 PR 待独立评审；PR #67 的转码实现尚未合并或完成媒体验收。
- 日期：2026-10-27；跟踪 [Issue #66](https://github.com/lalmax-pro/lalmax-nvr/issues/66)、[PR #67](https://github.com/lalmax-pro/lalmax-nvr/pull/67)。
- 实现核对基线：`34579a5317af5fa905eb324c682d41e3b4331f85`。本文是 P0 首批决策；架构文档中较宽泛的目标不代表已有实现。实际媒体验收见[验收记录](https://github.com/lalmax-pro/lalmax-nvr/blob/849198a0c9d4b5d9c00aef171c8bbf1e55f2d170/docs/zh/transcoding-testing.md)。

## 对象、映射与运行意图

| 对象 | 首批决策及当前行为 |
|---|---|
| SourceRef | `engine_id`、`app_name`、`stream_id` 指向已有源；当前仅支持 `embedded`，由服务端解析输入，不接受任意输入 URL。应用名属于身份，禁止把内部派生输出作为新源。 |
| Profile | `id` + `revision` 是任务固定引用；有效媒体参数和后端参与共享 hash，显示名及 revision 不参与。同一模板被任务引用时不能修改或删除；变更先解除引用，不承诺无缝切换。 |
| Job | `id` + `generation` 用于 CAS；启用状态、`always`/`manual` 和显式停止意图持久化。编辑每次递增 generation；被停止的 always 不因重启自动启动，显式启动成功才清除停止意图。manual 不在服务重启时自动启动。 |
| Variant | API 逻辑输出 ID 是 `nvr_tc_` + job ID；它不是播放地址。物理输出每次工作重启采用随机 ID，服务端保存来源映射；多个 Job 可引用同一物理输出。 |
| 共享与停止 | 共享键包含 engine、app、stream 和有效模板 hash。同一个 Job 的停止只释放其持有的需求；其他 Job 仍引用时不停止进程。当前没有独立的消费者租约，HA 发布撤销与全局停用的完整语义仍属 P3 验收。 |
| 版本水位 | 删除后的任务和模板保留各自版本水位；同名重建取大于旧版本的值，拒绝旧请求。水位和物理输出 deny-only 登记当前不自动清理，不能在没有防旧请求/旧令牌方案时直接删除；增长及有界保留方案留给 P1。 |

## 存储、迁移与回退

1. 当前实现通过 `CREATE TABLE IF NOT EXISTS` 初始化 `transcode_intent_v1` 和 `transcode_outputs_v1`，不复用历史跳过的转码迁移。前者存模板、任务及版本水位，文档级 revision 使用 CAS；后者登记物理输出 ID 和源 ID，不保存媒体令牌或进程 PID。
2. 新装/升级默认 `transcoding.enabled: false`，不自动从历史转码表导入任务；旧数据不被本功能自动删除。旧记录若需迁移，必须先有单独映射方案和测试，不能声称目前支持自动导入。已有 v1 文档缺少新增字段时以零值读取；重启后物理 ID 恢复为 deny-only，旧令牌不恢复。
3. 升级或数据库迁移前备份数据库和配置；切换前停写并验证目标数据。现有 SQLite 到 PostgreSQL/MySQL 的通用迁移会枚举源表并拷贝行，但**含转码数据的跨驱动迁移和恢复尚未验收**，不将其列为首批已验证能力。需验证表初始化、全部数据/水位、失败回滚及重启鉴权后才可声明通过。
4. 回退到不识别这些字段或表的旧程序，不能保证旧程序保留新的停止意图、版本水位及派生流 deny-only 语义；不得让旧版和新版同时写同一数据库。回退应先停止转码并恢复升级前一致的数据库/配置备份，再检验原始业务；这会丢失备份后的配置更改，不能宣称无损回滚。

## 权限和媒体通道

- 首批控制/查看转码模板、任务、variants 和管理员播放/预览 API 使用现有 `RequireOperatePermission`，即 `superadmin`；配置文件和程序路径由部署管理员控制。HA 走其既有集成认证和发布设置。并非任意已登录用户均能管理或获取派生输出。
- 决策目标是派生输出继承源访问权限且可收紧；当前实现**尚未提供通用的逐源授权继承**。在实现并验证该能力前，仅开放上述管理接口和经授权的 HA 发布路径，不声称普通用户具备完整派生输出权限。禁止通过输入任意地址或猜测物理 ID 绕过控制。
- embedded 内部链路：按源配置解析 RTSP 拉流令牌及可选 RTSP 账号；分配随机物理流和独立的临时 publish/playback 令牌后，经本机 RTMP 回灌至 `/live/{physical_id}`；媒体引擎观察到实际 H.264 帧后才报告 running。RTMP 监听必须由管理员启用，不能自动更改监听配置；外部媒体引擎和任意 worker 地址不支持。
- 临时令牌独立于源令牌；管理员 RTSP 播放 API 和 HA 目录可能返回包含播放令牌的地址，必须限制访问和缓存，不能将它描述成完全不返回凭据。同源 FLV 预览由服务端代理，内部令牌不下发浏览器。停止时撤销内部授权并尝试清理会话；不可将此等同于已验证所有消费者的撤销语义。命令行参数仍可能被同权限本机用户观察。
- 跨源/无权限拒绝、日志与诊断脱敏、发布和播放鉴权的全链路为 P1/P3 交付门槛；设计确定不等于这些测试已通过。

## 首批候选与验证矩阵

“候选”是计划交付或实验性代码可运行，不是发布承诺；每个平台仍需独立验收。不得把交叉编译或镜像构建等同于目标平台端到端通过。

| 环境与引擎 | 后端 | 当前证据和结论 |
|---|---|---|
| macOS ARM64、本地 embedded | FFmpeg / GStreamer | 两后端合成 H.264/HEVC 输入的 RTSP → 转码 → RTMP 回灌 → RTSP 实际解码已有记录；AAC/无音轨实际自检通过。GStreamer Homebrew 依赖进程级动态库路径。实体摄像机、长期运行未验收。 |
| Linux ARM64、Docker embedded | 默认镜像 FFmpeg / 可选 GStreamer | 可选 GStreamer 镜像构建、元素与有限编码 smoke test 已通过；默认 FFmpeg 和 GStreamer 的完整容器 API/回灌链路均未验收。`Dockerfile.arm64` scratch 镜像不包含后端工具，不在转码候选范围。 |
| Linux amd64/armv7、macOS amd64、本地或 Docker embedded | FFmpeg / 可选 GStreamer | 暂为候选，缺少对应环境的完整媒体和清理实测；不能标记为已验证。 |
| Windows amd64、本地 embedded | FFmpeg / 可选 GStreamer | 转码包曾交叉编译；无运行/媒体验收，Windows Job Object 进程树治理未完成，不列为首批已验收平台。 |
| 外部媒体引擎、独立 worker、硬编、派生 WebRTC | 所有 | 当前不支持或属于后续阶段；不作静默跨后端/软硬件回退。 |

默认 Dockerfile 包含 FFmpeg；`WITH_GSTREAMER=true` 安装 GStreamer 工具及 base/good/bad/ugly/libav 插件，并保留 ffprobe。构建参数用于本地构建，不指定已发布镜像名称或声称镜像已发布。

## API、界面命名与默认值

- 管理入口：`/api/transcoding/capabilities`、`/api/transcoding/profiles`、`/api/transcoding/jobs`，通过 profile `probe` 和 job `start`/`stop` 子路径执行操作；逻辑输出使用 `/api/streams/{stream_id}/variants` 及其 `play`、`stream.flv` 子路径。详细方法和权限以 [路由](https://github.com/lalmax-pro/lalmax-nvr/blob/849198a0c9d4b5d9c00aef171c8bbf1e55f2d170/internal/api/handler.go) 为准，命名对应 Profile（模板）、Job（任务）、Variant（输出）。
- 设置页使用“通用转码能力”和任务管理；当前界面支持默认模板及手动草稿，不代表已有通用模板编辑器、always 策略设置或完整原流/派生选择。HA 只保存任务引用，不改变源 ID、事件和 PTZ 身份。
- 全局默认关闭；最大并发工作数 2（配置范围 1–32，0 使用默认），每任务线程参数默认 2（1–16，0 使用默认）；它们不是 CPU/内存硬隔离。默认模板为 H.264、上限 1280×720、15 fps、1500 kbps、GOP 30，AAC 48 kHz/单声道/64 kbps。
- 后端启动阶段及首个输出各最多 20 秒；running 后媒体健康容忍 10 秒，帧需在最近 5 秒内更新。停止进程有界等待并强杀；清理最多 3 次、每次 1 秒。GStreamer 源尺寸探测最多 8 秒。
- 自检发现阶段最多 15 秒、单个探测命令最多 10 秒；FFmpeg 媒体自检最多 30 秒，GStreamer 最多 40 秒；成功结果缓存 10 分钟，配置指纹变化即失效。后台协调每 2 秒检查一次，单轮上下文最多 45 秒；失败重试延迟从 5 秒倍增至 60 秒，当前无抖动且没有固定总重试次数。禁止把这些实际行为误写成已实现抖动或有限重试预算。

## 自审结论和后续门槛

P0 已按当前代码和证据整理源/输出身份、版本与迁移、权限与回灌、平台候选和接口/资源默认值。独立评审与 PR 合并仍未完成，不能代替评审人批准。P1–P3 应分别完成权限继承、跨驱动迁移测试、版本登记保留策略、平台媒体与异常恢复、浏览器/真实 HA/实体设备验收；其未完成状态不得因 P0 收敛而勾选。
