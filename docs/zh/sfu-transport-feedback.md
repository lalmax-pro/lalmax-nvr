# SFU transport-cc 反馈基础

NVR SFU 和 Meet 示例在 Pion PeerConnection 上启用 transport-wide RTP 头扩展发送器。对协商了该扩展的客户端，出站 RTP 获得逐包递增的传输序号；不支持该扩展的客户端不会获得此头扩展。Pion 的默认接收反馈拦截器仍负责处理 RTCP，NACK/PLI/FIR 路径保留。

验证：

```sh
go test -race ./cmd/lalmax-nvr -run 'TestNVRSFU(EmitsTransportCCAndReceivesFeedback|WorksWithoutTransportCCNegotiation|RecoversViewerPacketLossWithNACK)' -count=10
go test -race ./tools/sfu-meet -run TestDemoServerOpusThroughTwoPeers -count=10
```

本机 Pion 测试验证视频 RTP 扩展序号递增并收到 transport-cc RTCP 反馈，不协商扩展的接收端仍可收包，Meet Room 测试检查静音 Opus RTP 含扩展；测试不访问设备或播放声音。NVR 可通过配置 `sfu.observe_bandwidth: true` 启用实验性的 Pion 发送端带宽估计，并将每个连接的估计变化以 `bitrate_bps` 输出至 debug 日志（需设置 `observability.log_level: debug`）。配置默认关闭；估计按 PeerConnection 汇总，初始值为 1 Mbps，既不是单条轨道，也不是经过测量或保证可用的网络容量。仅开启观测时不缓存或延迟 RTP，轨道解除绑定和连接关闭时清理引用。

```sh
go test -race ./cmd/lalmax-nvr -run 'Test(SFUBWEPassThrough|NVRSFUBandwidthObserver)' -count=20
```

本机 Pion 测试验证估计值变化、无 transport-cc 客户端兼容以及 NACK 共存；它不代表真实网络估计准确性。还可设置 `sfu.pacing: true` 开启实验性的按连接发送节奏控制；此选项要求同时启用 `observe_bandwidth`。发送初始目标为 1 Mbps，后续使用带宽估计更新；同一 PeerConnection 上音视频共用预算，当前没有音频优先级；预算包括 RTP 头、负载及填充，不包括 SRTP、UDP 和 IP 开销。pacer 不复制或排队媒体数据，而是阻塞发送方，限制每连接最多 64 个等待写入及最多 200 ms 的预约等待；单个 RTP 包在当前目标速率下超过 200 ms 时也会拒绝。持续拥塞可能使该连接的订阅发送停止，需重新订阅；它不降低发布者码率，也不切换层。默认关闭。

```sh
(cd third/lalmax && go test -race ./sfu/pacing -count=20)
go test -race ./cmd/lalmax-nvr ./internal/config -run 'Test(NVRSFUPacing|SFUPacing)' -count=20
```

pacing 的本机测试覆盖有界等待、关闭和连接隔离，以及 Pion TWCC/NACK 共存；真实弱网下的速度、公平性、音视频质量及恢复仍需验收。Simulcast 选层、源端码率适配、公网和浏览器兼容性仍未完成。
