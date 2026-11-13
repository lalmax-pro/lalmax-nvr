# Issue #66 P4/P5：上游代码审查与验收边界（2026-10-31）

本提交是以原仓库 `lalmax-pro/lalmax-nvr` 的 `main`（基线 `273d83313ae974443807f36c85d60041a6602302`）为起点，对 Fork 的 P4/P5 实现进行独立移植。**不应把 Fork 上针对定制环境的通过结果直接当成上游的 CI 或生产验收。**

## 修改点与新增功能点

| 模块 | 新增或修改 |
| --- | --- |
| P4 / 按需转码 | 新增 `on_demand`、租约获取/续期/释放、首次媒体就绪等待、最后消费者退出后的回收、任务代次隔离与退避重试 |
| P4 / HA 预加载 | 可选 `preload_transcode`，发布期间维护消费租约；取消发布、修改 Job 或切换代次时撤销 |
| P4 / WHEP 有声音频 | H.264 + PCMA/8000 SDP 校验、ICE/DTLS/RTP 播放，G.711 A-law 后端输出和规范化，认证加密会话及 WHEP DELETE |
| P4 / 后端 | GStreamer 输出健康检测使用新鲜帧或两次媒体字节增长，避免只靠旧 FPS 或非零计数误报就绪；支持显式 NVENC 后端并要求实际探测，失败不得静默回退 |
| P4 / 多档共享解码 | **仅设计评估文档**，不是已启用的共享解码实现 |
| P5 / 录像 | 手动派生录像的开始/停止，保存逻辑源、Job、Generation、Profile 和实际编码来源；不改变默认录像策略 |
| P5 / 转推 | Relay 可绑定派生 Job，运行时解析受保护的物理输出；按输出流量和媒体载荷分别统计，避免在日志或数据库中泄漏令牌 |
| P5 / Worker | 一次性本机独立 Worker 和认证 stdin 控制协议，进程清理及固定模板；**同 UID 的进程级隔离，不是 seccomp 或特权隔离** |
| Web UI | 明确选择原流/派生流、按需租约维护、HA 预加载开关、转码任务及派生 Relay/录像操作 |
| 安全/一致性 | 派生输出撤销授权优先于可能失败的录像关闭；SQL `enabled` 使用整数绑定；内部输出不可匿名读写或跨源复用 |

## 已有证据（Fork 实测，非上游移植分支实测）

- Fork 上已执行的 GStreamer 30 轮测试：**21 轮实际媒体通过，9 轮负控或状态检查**；RTSP → 转码 → RTMP → 回放、ffprobe/ffmpeg 实际解码通过。
- Chromium 直接接入 **NVR 端 HA-WHEP**：H.264 + PCMA/8000、ICE/DTLS、实际视频 RTP/解码、PCMA RTP 及 Web Audio 非静音样本通过；Pion 原生客户端也通过。
- Fork Go vet、Go race、前端 404/404、Linux/Windows/macOS 的 CI 构建通过。代表性 Fork 提交：`b67407dacac9909fa4d1f9c102d02e78d7ec846f`。
- 这些结果发生在 Fork 的原生隔离 Linux 环境；本上游移植分支需要**重新运行上游 GitHub Actions 和针对性回归**，未完成时必须标记为待验证。

## 两项尚未实测的环境门槛

1. **真实 Home Assistant：BLOCKED_ENVIRONMENT / NOT_TESTED。** 已验证的是 NVR 侧 HA API 和直接浏览器 WHEP，并非真实 HA 实例中的实体发现、云台、事件、预加载、音视频、取消发布的全链路验收。
2. **真实 PostgreSQL / MySQL：BLOCKED_ENVIRONMENT / NOT_TESTED。** SQLite 实测及其他方言 SQL/参数类型检查不等于真实 PostgreSQL/MySQL 服务器上的 Schema 初始化、升级、读写和事务验收。

## 其他显式边界

- `StreamDetail` 的派生预览仍为 HTTP-FLV/mpegts.js，**PCMA 音频不能在这条预览路径播放**。Chromium 直接 WHEP 测试通过，不能推断 FLV/PCMA 兼容。
- NVENC 要求当前宿主真实编码器/驱动能力测试，未测试的 GPU 不承诺支持；多档共享解码和历史录像离线转码不属于本次已实现范围。
- 真实生产摄像机完整矩阵、长稳、Docker 全链路及部署验证未覆盖。默认转码仍关闭，未改动生产 NVR。
- 本移植分支不包含 Fork 独有的流量监控、本地视频、其他 GB28181 及 UI 定制；审查应以原仓库 `main` 为基准。

## 审查建议

优先复核安全令牌生命周期、WHEP 源绑定及会话加密、代次隔离、租约回收、进程与录制资源释放、数据库跨驱动迁移、前端派生预览失败不回落原流；要求上游独立 CI 全绿后再决定合并。**不要因为当前 Fork 已通过便直接认定上游合并可以发布生产。**
