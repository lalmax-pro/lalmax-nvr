# 通用转码实施与验收记录

关联 [Issue #66](https://github.com/lalmax-pro/lalmax-nvr/issues/66)；设计见 [架构文档](transcoding-design.md)。

> 当前仅为实施中的局部验证，不代表 P1–P3 已完成。未发布、未合并。

## 2026-10-27：分支 API 实际流端到端

在独立数据库、专用随机管理员凭据及专用端口上启动本分支二进制。未修改既有 V1.0.3。定位到内嵌配置补丁强制覆盖显式 HTTP/RTSP 地址，导致连接错误引擎；已改为仅在地址为空时补默认值。

- 从现有 NVR 鉴权 RTSP 的 `ha-test-main` 拉入分支 `real-source`（这是运行中的合成测试发布流，不是实体摄像机，不能记作真机验收）。API 创建返回 201，源状态 active/H264。
- 通过 API 创建 FFmpeg 模板、自检、启用手动任务、启动，任务进入 running。
- 派生 RTSP 实际取回并以 FFmpeg 解码 3 秒，退出码 0；ffprobe 确认 H.264 640x360、AAC 48000。删除播放 token 后访问失败，鉴权拒绝通过。
- GStreamer 首次同链路 output_timeout，实际诊断显示 rtspsrc AAC 动态 pad delayed linking 失败。修复为视频/音频 caps 后立即加 queue，避免单路阻塞源暴露另一路。重新构建后，通过同一持久化任务 API 自检、启动进入 running；派生 RTSP 解码 3 秒退出 0、无解码错误，ffprobe 确认 H.264 640x360 / AAC 48000。无 token 访问被拒绝，停止返回 204。此前失败保留为修复前证据，不混作通过。
- 共享链路实测：两个同源同模板任务返回相同 work key/物理输出；停止第一个后第二个输出实际解码退出 0；最后一个停止后旧地址拒绝访问。
- 此次复测发现进程重启后旧派生流从历史目录重新出现；新增持久化物理身份注册表，发布前写入、启动时恢复 deny-only 授权标记，不靠前缀猜测。数据库关闭/重开测试证明标记可恢复，相关 race 测试通过；历史上未登记的试验流不会自动按前缀追认。后续使用最新分支二进制执行实际进程启动→API 转码 running→停止进程→重启，确认新生成物理输出不再出现在普通目录，旧带 token RTSP 被拒绝，脚本输出 restart_registry_e2e=passed；重启测试实例已清理。
- 常驻任务重启恢复实测：将 FFmpeg 手动任务改为 always（generation 2），停止进程后重新启动，不调用手动检测；后台自动自检、恢复 running，取回输出解码 3 秒退出 0，输出 always_restart_e2e=passed。测试结束通过 API 停止、恢复 manual（generation 3）并关闭隔离实例。
- HA 发布 API 实测：发布 real-source 并选择 FFmpeg 派生任务，HA 目录保留源 ID、报告实际 H264 且禁用派生 WebRTC；目录 RTSP 实际解码退出 0，快照接口返回 19460 字节有效 JPEG，停止任务后目录离线且 URL 清空。脚本输出 ha_publication_api_e2e=passed。仅验证 NVR 的 HA 接口，不等同于真实 HA HLS/浏览器播放；设置已恢复，实例关闭。
- 网页同源 FLV 接口实测：管理员 HTTP 请求取回派生 FLV，FFmpeg 从该 API URL 解码 3 秒退出 0，无解码错误；匿名请求返回 401。仅证明代理/媒体链路有效，真实浏览器 MSE 播放仍需确认。停止任务并关闭隔离实例。
- Chrome 实际 MSE 补测：独立 HeadlessChrome 156，经项目已安装 mpegts.js 播放鉴权同源派生 FLV（私有临时代理提供账户头，不把内部 token 交给页面）；currentTime 增至 3.097 秒、readyState=4、videoWidth=640、totalVideoFrames=50、播放器错误数组为空。第一次尝试等候超时，第二次带进度诊断后通过，保留首帧启动时间的不确定性。此为真实浏览器/播放器媒体验证，不等同于完整设置页按钮交互或 HA 页面验收。NVR、临时代理与独立 Chrome 均由脚本清理。
- HEVC 实际输入补测：ffprobe 确认现有 NVR 的 ha-test-second 为 HEVC/AAC，拉入隔离分支 hevc-source 后分别通过 API 创建 FFmpeg、GStreamer 任务，两者进入 running 并各取回输出解码 3 秒退出 0，脚本输出 hevc_input_dual_backend_e2e=passed。仍为合成发布流，不标记为真机；任务已停止、实例关闭。
- 两个任务的停止 API 均返回 204，随后终止隔离实例。测试凭据和媒体地址仅保留在私有临时目录，无真实图像产物入库。
- 一次 FFmpeg 解码诊断使用了当前版本不适用的 `-rw_timeout` 导致退出 8，去除该参数后实际解码通过；不将前者记为媒体不兼容。

## 2026-10-26：领域、进程与能力检测基础

- 分支：`feat/66-general-transcoding`。
- 环境：本地 macOS ARM64，Go 1.26.2，FFmpeg 8.0.1（Homebrew）。
- 新增模板/任务模型、参数校验、有效模板哈希和跨引擎/应用的去重键。
- FFmpeg 固定参数构造，进程停止/取消/回收测试；Unix 进程组清理，Windows Job Object 尚未实现。
- 能力检查有总时间限制、子进程时间限制和有界输出。声明能力不等于实际转码可用。

### 已执行并通过

```sh
go test -race ./internal/transcode
NVR_TRANSCODE_MEDIA_TEST=1 go test -race -v ./internal/transcode
```

实际 FFmpeg 自检：合成 320×240 视频，分别使用 AAC 音轨/无音轨，编码 FLV，ffprobe 检查编码/尺寸/音频规格，再用 FFmpeg 实际解码。两个场景通过。该测试不接触真实摄像机，也不发布到媒体引擎。

### GStreamer 环境诊断（未通过能力验收）

本机存在 `gst-launch-1.0` / `gst-inspect-1.0`，但直接版本/插件检查发生停滞，scanner 报告 GLib/GObject 动态库加载失败以及 `can't find gi.repository.Gst`。已终止无界诊断命令，未修改或重装宿主机依赖。

```sh
NVR_GSTREAMER_MEDIA_TEST=1 go test -race -v ./internal/transcode \
  -run 'TestGStreamerRealDiscovery|TestDiscover|TestBounded'
```

上面测试验证的是**故障诊断行为**，不是 GStreamer 转码成功：检测在约 15 秒预算内结束，程序检查标记 failed，未执行插件检查标记 not_tested，CanEnable 为 false。该诊断测试通过；GStreamer 实际编码尚未验证。

### GStreamer 实际自检补测（同日）

未重装或修改系统依赖，仅为测试进程设置 `DYLD_FALLBACK_LIBRARY_PATH=/opt/homebrew/lib` 后，GStreamer 1.26.9 版本检查成功（仍有 GTK3/GTK4 重复类警告）。执行：

```sh
DYLD_FALLBACK_LIBRARY_PATH=/opt/homebrew/lib NVR_GSTREAMER_SELFTEST=1 \
  go test -race -v ./internal/transcode -run TestGStreamerActualSelfTest
```

H.264/AAC 和纯 H.264 两项实际自检通过：GStreamer 编码、ffprobe 检查、GStreamer 视频/音频解码。此为本地能力验证，**不是实际摄像机 RTSP 输入/RTMP 回灌/HA 播放验收**。测试环境变量没有写入全局配置或运行中的 NVR。

### 扩展回归（同日）

- `go test -race ./internal/transcode ./internal/auth ./internal/ban ./internal/config ./internal/storage ./internal/media ./internal/api ./cmd/lalmax-nvr` 全部通过（8 个受影响包的完整测试，不只是名称筛选）。
- 前端 TranscodeJobs / TranscodingApi / TranscodingSettings 三个测试文件共 6 项通过，覆盖禁用手动草稿、模板 revision、删除取消、204 空响应、显式检测和过期状态。
- 前端完整测试 `npm test`：33 个测试文件、388 个测试通过；生产构建通过。既有 VoIP/Svelte 警告和测试环境相对 URL 日志不影响退出码，未新增转码失败。
- 尚未进行真实浏览器操作与实际源媒体联调。
- 复测真实源时发现当前运行的 NVR 是已发布 V1.0.3 二进制，`/api/transcoding/capabilities` 返回 404；因此不能用其验证本分支新增 API。独立 FFmpeg→RTMP 回灌过程可启动但无法从 RTSP 取回输出，尚未判定为通过。- 真实已运行 NVR 源探测：`ha-test-main` 经 RTSP Digest/播放 token 取回，ffprobe 确认 H.264 640x360 + AAC 44100，证明测试源、RTSP 鉴权和源编解码可用。随后使用独立随机输出名尝试本机 FFmpeg RTSP→RTMP 回灌；FFmpeg 进程可正常结束但通过 RTSP 取回派生输出未成功，尚未判定为转码链路通过，保留为失败待查，不修改运行中的 NVR 配置。
- 尝试用本分支二进制启动隔离实例时，复用了已有 `lalmax.json`/WebRTC 端口，因 UDP 4888 已被占用而初始化失败；未继续抢占端口或影响现有实例。需要生成独立媒体引擎配置并关闭/迁移全部冲突监听后再做端到端测试。
- 后续已生成独立 `lalmax.json` 并关闭 WebRTC/GB28181；首次仍碰到 19190 端口被另一个进程占用，改用 19290 后分支 NVR 健康检查成功，`/api/transcoding/capabilities` 确认已存在但返回 401。由于隔离实例认证明文凭据不在当前可读运行目录，未绕过认证继续调用，也未修改现有服务。
- Docker 实际验证：`docker build --build-arg WITH_GSTREAMER=true -t lalmax-nvr:transcode-gst-check .` 成功（Docker Desktop linux/arm64）；显式 shell smoke test 确认 GStreamer 1.24.11、x264enc、avenc_aac、FFmpeg 6.1.2 可用，并完成有限帧 H.264 编码。该镜像未推送，容器运行 NVR 的默认 entrypoint 未作为媒体验收。
- 复核时后端受影响包普通测试通过；首次将 npm 命令误在仓库根目录执行（无 package.json，退出 254），随后在 `web/` 正确执行，4 个转码/HA 测试文件、8 项测试通过。该误执行不影响代码。
- `go test ./...` 已执行：转码及其他大多数包通过；现有 `internal/ftp` 的 12 项路径安全测试因 macOS `/var` 与 `/private/var` 临时目录前缀差异失败，失败与本次转码改动无关；完整命令退出码为 1。

### 路径与平台补测

- 快照解析程序路径改为优先遵循 `transcoding.ffmpeg_path`，显式路径无效不回退 PATH；新增回归测试通过 race 检查。
- 转码包测试程序 `GOOS=windows GOARCH=amd64 go test -c`、`GOOS=linux GOARCH=arm64 go test -c` 交叉编译通过；这不是目标平台运行验收。Windows Job Object 进程树回收尚未实现。

### 安全复核补充

独立只读审查发现预先订阅可预测物理流名及清理失败丢失注册信息两项风险，随后修复：物理 group 每次执行使用随机身份（逻辑输出 ID 不变）；任何同名 group 均拒绝预留；撤销凭据后清理失败保留注册信息，并作有限重试。新增非活跃 group 订阅碰撞、重启身份轮换、失败清理可重试测试均通过 race 检查。源身份过滤仍需进程重启/历史数据场景的进一步验证。

独立审查另发现 generation 水位在停止后阻塞未启动的新 generation，以及 RTMP 配置指针浅拷贝问题；已修复并增加“先停止再保存/停止”的回归覆盖。源标识现拒绝 URL 分隔符、反斜杠、控制字符和过长值，防止改变派生 URL 结构。

HA 快照已改为使用所选派生输出并隔离缓存；已测试输出不可用时不返回旧原流图片、不回退原流。真实 HA 图片/视频仍未实测。

### 当前未覆盖 / 后续工作（以本节及 2026-10-27 记录为最新状态）

- 海康实体摄像机 H.264/AAC 已完成 FFmpeg 视频 copy + 音频转码的有限链路测试；GStreamer、实体 H.265、长时间稳定性及真实公网仍未验收。H.264/HEVC 合成发布流不混作真机。
- 完整设置页按钮交互、实际 FlvPlayer Svelte 组件生命周期仍待浏览器验证；Chrome/mpegts.js MSE 媒体播放已通过。
- 真实 Home Assistant 实例的 HLS/浏览器播放未验收；NVR 面向 HA 的目录、RTSP、JPEG 和离线语义已实际通过。
- FFmpeg/GStreamer 的 RTSP 输入→转码→RTMP 回灌→派生 RTSP 解码、共享任务、常驻重启恢复、身份隔离重启均有实际通过证据，见顶部记录。
- 检测缓存仅保存在内存；已启用 always 任务重启时自动重新自检恢复已实际通过，手动任务仍需管理员检测后显式启动。
- FFmpeg copy 模式现通过内置合成样本自检；GStreamer copy 模式自检仍未支持。实际源限制和链路验收另行验证。
- 源尺寸探测暂使用 ffprobe，GStreamer 路径尚非完全独立依赖。
- Docker linux/arm64 构建/有限编码已通过；完整容器 API 媒体链路、其他架构运行、Windows Job Object、外部引擎和硬件编码未验收。
- 源服务、日常浏览器和运行中的旧版 NVR 均未替换；隔离测试服务已清理。

### 审查问题复测（2026-10-27）

- 显式停止意图写入任务持久化数据；重建服务和关闭重开数据库后，常驻任务不会重新自检或启动。再次显式启动才清除该意图。
- 删除同名任务或模板时保留版本水位，重建后的 generation/revision 高于旧值；旧的更新、删除和停止请求被拒绝。版本水位随已删除 ID 累积，后续需设计安全的保留策略，不能删除仍可能被旧请求使用的水位。
- 在 macOS ARM64 上实际运行 `NVR_TRANSCODE_MEDIA_TEST=1 DYLD_FALLBACK_LIBRARY_PATH=/opt/homebrew/lib NVR_GSTREAMER_SELFTEST=1 go test -count=1 -v ./internal/transcode -run 'Test(FFmpegRealSelfTest|GStreamerActualSelfTest)'`，FFmpeg 和 GStreamer 的 AAC、无音轨编码/探测/解码自检均通过。此项使用合成数据，不代表本次重新执行 RTSP 回灌、真实摄像机或 HA 端到端验收。

### P1：FFmpeg copy 与海康真实源（2026-10-27）

- FFmpeg copy 模式使用内置的无隐私合成 H.264/AAC 样本自检。macOS ARM64 / FFmpeg 7.0.2 实际验证四组：视频 copy + AAC 转码、视频 copy + 无音频、H.264 编码 + 音频 copy、音视频均 copy；均经 ffprobe 检查及 FFmpeg 解码。自检不能代替源兼容及引擎回灌。
- Linux ARM64 隔离容器中，基于已存在的 `ghcr.io/lalmax-pro/lalmax-nvr:1.0.3` 运行镜像替换为当前工作树编译的二进制，FFmpeg 6.1.2；该测试镜像未推送，不将其记作完整默认 Dockerfile 构建通过。API 新建视频 copy + AAC 模板，能力检测、任务运行、合成源 RTSP → 转码 → RTMP 回灌 → 派生 RTSP 实际解码通过。停止合成源、检测任务异常、重新发布后，always 任务自动恢复并再次解码通过。
- 经用户授权只读获取本地海康 ONVIF/RTSP 流，原始视频为 1920×1080 H.264、音频 AAC；通过独立 FFmpeg 直拷转入隔离 NVR RTMP 输入，运行同一视频 copy + AAC 转码任务，派生 RTSP 解码 3 秒成功，ffprobe 确认 H.264 1920×1080 + AAC。没有保存画面或修改摄像机、运行中的 NVR 或录像配置。
- 第一次从合成源直接切换海康源时，任务状态显示 running，但派生解码失败；当时没有保存足够诊断信息，原因未确定。两次后续测试在等待旧任务检测断流后重新接入海康源，均通过，不把首次失败记作通过。
- 已将 RTMP 未启用、源离线等安全错误码从异步后端启动传递到任务状态；未知原始错误仍映射为通用错误码，避免暴露凭据。对应单元和 race 测试通过。
- 当前工作树默认 Dockerfile 在 Linux ARM64 构建成功，FFmpeg 6.1.2；镜像未推送。独立容器的 API 建模板、copy 自检、合成源回灌/RTSP 解码、源断开后恢复，以及海康真实源（经宿主 FFmpeg 直拷接入）回灌和解码均通过。
- 默认镜像另以没有音轨的合成 RTMP 发布源启动视频 copy + AAC 模板，派生流经 RTSP 实际解码，并由 ffprobe 确认 H.264 视频且没有音轨。独立的 RTMP 禁用实例使用 RTSP 发布源，任务状态返回 `rtmp_disabled`。
- 默认镜像第一次链路中的媒体解码通过，但测试脚本停止临时海康拉流进程时超时，导致整轮脚本退出失败；修正有界强制清理和错误脱敏后重跑，上述整轮测试通过。未修改摄像机配置，临时进程和容器已清理。
