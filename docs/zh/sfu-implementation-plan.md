# SFU 分批实施计划

跟踪：[Issue #93](https://github.com/lalmax-pro/lalmax-nvr/issues/93)。首版架构与延期边界见 [P0 首版决策](sfu-p0-decisions.md)。以下区分已实现能力与仍需真实环境验收的事项；本机合成测试通过不代表生产验收。

## 当前实现状态（2026-11-10）

1. **Room、轨道与控制**：内存房间、generation、容量限制、发布/订阅、快照和事件流已实现；NVR 提供多房间管理和可选空闲回收。Meet 在信令或媒体连接失败后可换新票据和 PeerConnection 重连，保留仍存活的摄像头/麦克风及静音状态；屏幕共享需手动重新开启。没有服务端会话续接，真实媒体在弱网下恢复仍需验证。
2. **纯 SFU 链路**：H.264/Opus 双向 RTP 转发、动态订阅、PLI/FIR 上游反馈和 Pion 默认 NACK 重传已有实现。NVR 与 Meet 同时配置出站 transport-wide RTP 序号和入站 Transport-Layer-CC 到达反馈；NVR 为每个观看端收集 GCC 估算并独立选择摄像头 RID。带宽不足时立即降层；根据编码 cap 仅选用不超过估算值 80% 的层，升层还需连续两秒满足 125% 余量。Meet 摄像头发送 l/m/h 三层（150/500/1500 kb/s 上限）；切层后连续重写输出序号并请求新层关键帧。NVR RoomFactory 闭环 race 测试以两个本机订阅者和 50% 合成丢包重复五次，验证受损者降至 l、健康者维持 m；真实弱网画质仍待验收。Screen share 仍为单层；不修改源端编码码率。详见 [transport-cc 与反馈验证](sfu-transport-feedback.md) 及 [丢包恢复验证](sfu-nack-validation.md)。
3. **GroupBridge**：单条音频/视频轨道及同一成员的音视频组合可桥接到独立 Group；NVR 提供受保护的桥接管理、WHEP 播放和录像计划链路。除合成录像、Opus 中断补静音和约一分钟标记对齐外，2026-11-10 已用 Chrome 真实摄像头/麦克风通过 NVR Meet、GroupBridge/WHEP 和临时录像计划生成并解码五段 H.264+Opus MP4。五段源包 DTS 严格递增；首段音视频轨起始相差约 839 毫秒，后续段相差 125–176 毫秒，故首段对齐及重复启停后的稳定性尚未通过。人工听音、弱网和长时间同步仍需验收。详见 [桥接录像验证](sfu-bridge-recording-validation.md)。
4. **Meet Demo**：支持设备选择与会中切换、摄像头/麦克风/屏幕共享、远端轨道动态订阅、NVR 多房间管理、桥接播放、重连和浏览器网络诊断。2026-11-10 在 Chrome 本机双标签通过 NVR 鉴权页面以真实摄像头和麦克风入会；接收端收到 H.264/Opus，另一标签发布的屏幕共享作为独立 H.264 轨道到达接收端，停止共享后该轨清理。订阅取消/恢复、正常离会及最后房间回收至 0 人均已验证。摄像头 Simulcast 诊断仍为 l/m 有发送、h 为 0，故不能确认三层实际上行或真实弱网画质。此项为本机回环短时验收；真实媒体中断恢复、三名以上真实浏览器成员、人工听音和较长时间同步仍待完成。
5. **质量与容量**：Pion transport-cc/GCC、可选 per-peer pacer、认证 TURN relay 本机验证、Simulcast 选层代码和最多 31 个观看端的本机负载基线已具备。负载数据使用同进程 Pion 客户端和不可解码的合成 RTP；不能外推为公网带宽、真实解码负载或生产容量。Simulcast 真实弱网画质、源端码率适配、跨 NAT/公网和多浏览器验证仍待完成。当前数据不足以决定 Group → SFU 适配、多节点或扩展编解码。

## P2：会议与稳定性剩余验收

- 本机 NVR 鉴权入口的真实设备摄像头、麦克风、屏幕共享、接收、GroupBridge/WHEP 和录像计划已验收，五段 MP4 均可全轨解码；具体数据及首段约 839 毫秒起始偏差见[桥接录像验证](sfu-bridge-recording-validation.md)。另以 `SFU_NVR_MULTI_MEMBER_TEST=1 go test -race ./cmd/lalmax-nvr -run '^TestNVRSFUAuthenticatedThreeMemberDynamicSubscription$' -count=3` 验证 NVR 管理员 HTTP 票据、WebSocket 信令及三名本机 Pion 成员；晚加入的观看者能收到已在发布的 RTP，同一会话取消后重新订阅会恢复新轨道和 RTP，一名离开后房间回收至两人、订阅数降为一，剩余观看者持续收包。该测试用合成 RTP、不打开设备。
- 剩余 P2 验收是重复启动/停止后核对首段音画起始偏差，人工听音、第三方播放器、真实弱网和长时间同步；真实媒体中断恢复、三名以上真实浏览器成员以及异常设备资源清理也没有覆盖。当前回环测量不能代替其他网络和设备环境。

## P3：质量与容量剩余工作

- 在实际弱网下验证 Simulcast 画质、切层延迟与关键帧恢复；当前按每位订阅者的 GCC 估算自动选接收 RID，不控制摄像头源端编码码率。代码闭环测试已验证受损订阅者降层、健康订阅者保持较高层；Chrome 实机仍只观察到 l/m 有发送、h 为 0，完整三层上行和真实弱网体验仍待验收。
- 在实际公网 NAT/TURN 环境和目标浏览器上验证 ICE 连通与媒体播放；本机 relay-only 测试仅证明本机 TURN 基础可用，见 [TURN 验证](sfu-turn-validation.md)。
- 在目标部署机测量实际编码媒体下的并发订阅、码率、包速率、CPU、内存、网络出口和首帧延迟；现有本机 31 观看端数据见 [Pion 负载基线](sfu-pion-load-baseline.md)，只代表本机合成 RTP 传输。
- 用代表性负载数据决定是否推进 Group → SFU 适配、多节点或其他编码支持；本机小规模合成负载不足以支持容量或架构承诺。

2026-11-10 本机真实媒体浏览器诊断曾显示约 1.01–1.76 Mb/s 接收媒体负载、0% 接收丢包和 0–1 ms 回环 RTT；该双标签页短时观察不用于估算出口带宽或生产容量，且无人工听音/同步仪器测量。详细范围见 [Pion 负载基线](sfu-pion-load-baseline.md)。

2026-11-10 Meet 网络诊断面板新增按 `outbound-rtp.rid` 汇总的视频分层发送码率及浏览器提供的编码状态字段；前端测试覆盖 RID 汇总、状态显示、采样清理和延迟结果隔离。Simulcast 接入后的 Chrome 实机报告 l/m 发送、h=0；具体范围见 [Pion 负载基线](sfu-pion-load-baseline.md)。
