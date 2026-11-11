# SFU transport-cc 反馈基础

NVR SFU 和 Meet 示例在 Pion PeerConnection 上同时启用 transport-wide 序号发送器与到达反馈拦截器。前者给出站 RTP 写入逐包递增的传输序号；后者为入站 RTP 生成 RTCP Transport-Layer-CC 到达时间反馈，供 GCC 估算器使用。客户端未协商该扩展时不会发送或期待 TWCC 头扩展。Pion 的默认接收反馈拦截器仍处理 RTCP，NACK/PLI/FIR 路径保留。

验证：

```sh
go test -race ./cmd/lalmax-nvr -run 'TestNVRSFU(EmitsTransportCCAndReceivesFeedback|WorksWithoutTransportCCNegotiation|RecoversViewerPacketLossWithNACK)' -count=10
go test -race ./cmd/lalmax-nvr -run '^TestNVRSFUReportsTransportCCForIncomingPublisher$' -count=3
go test -race ./tools/sfu-meet -run TestDemoServerOpusThroughTwoPeers -count=10
```

本机 Pion 测试验证出站 RTP 扩展序号递增、发布端入站 RTP 能触发 Transport-Layer-CC 反馈、不协商扩展的接收端仍可收包，Meet Room 测试检查静音 Opus RTP 含扩展；测试不访问设备或播放声音。NVR 为 GCC 驱动的摄像头接收层选择始终内部收集每个 PeerConnection 的带宽估算；配置 `sfu.observe_bandwidth: true` 会将估算变化以 `bitrate_bps` 输出至 debug 日志（需设置 `observability.log_level: debug`）。初始值为 1 Mbps，估算按 PeerConnection 汇总而非单条轨道，也不是经过测量或保证可用的网络容量。选择器只依据公布的 RID 码率上限换层，不调节浏览器摄像头的源端编码码率。未启用 pacing 时不会排队或延迟 RTP，轨道解除绑定和连接关闭时清理引用。

```sh
go test -race ./cmd/lalmax-nvr -run 'Test(SFUBWEPassThrough|NVRSFUBandwidthObserver)' -count=20
go test -race ./cmd/lalmax-nvr -run '^TestNVRSFUGCCSelectsSimulcastLayerPerViewer$' -count=5 -v
```

本机 Pion 测试验证估计值变化、无 transport-cc 客户端兼容以及 NACK 共存；它不代表真实网络估计准确性。还可设置 `sfu.pacing: true` 开启实验性的按连接发送节奏控制；此选项要求同时启用 `observe_bandwidth`。发送初始目标为 1 Mbps，后续使用带宽估计更新；同一 PeerConnection 上音视频共用预算，当前没有音频优先级；预算包括 RTP 头、负载及填充，不包括 SRTP、UDP 和 IP 开销。pacer 不复制或排队媒体数据，而是阻塞发送方，限制每连接最多 64 个等待写入及最多 200 ms 的预约等待；单个 RTP 包在当前目标速率下超过 200 ms 时也会拒绝。持续拥塞可能使该连接的订阅发送停止，需重新订阅；它不降低发布者码率，也不切换层。默认关闭。

```sh
(cd third/lalmax && go test -race ./sfu/pacing -count=20)
go test -race ./cmd/lalmax-nvr ./internal/config -run 'Test(NVRSFUPacing|SFUPacing)' -count=20
```

pacing 的本机测试覆盖有界等待、关闭和连接隔离，以及 Pion TWCC/NACK 共存；真实弱网下的速度、公平性、音视频质量及恢复仍需验收。Simulcast 的多 RID 转发、按订阅端估算选层和关键帧请求已有代码及 Pion 合成回归。Chrome 同机真实摄像头复测的 RID 统计中 l/m 有非零发送码率、h 为 0；完整三层上行、高层发送、真实弱网切层画质、源端码率适配、公网和多浏览器兼容性仍未完成。

2026-11-10 新增 NVR RoomFactory 闭环测试：一个 Pion 发布端发送带 l/m/h RID 的合成 H.264 标记，两个真实本机 PeerConnection 订阅者分别接收；向其中一个接收端的 ICE UDP 入站方向注入 50% RTP 丢包，由与 NVR 运行时共用的 GCC 估算回调更新成员带宽并驱动 Room 选层。race 测试重复 5 次全部通过：健康端始终停在 RID m，估算 1.327–1.368 Mb/s；受损端切到 RID l，估算 0.469–0.499 Mb/s，每次注入观察到 67–85 个丢包。该测试验证估算到 RID 的控制闭环和观看者隔离，不解码画面，不能代表真实弱网画质、浏览器源端码率适配或公网质量。另有专门回归确认 NVR 为摄像头发布端生成入站 TCC 反馈，避免只验证观看端入站反馈而漏掉发布端的 GCC 输入。
