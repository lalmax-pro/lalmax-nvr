# SFU transport-cc 反馈基础

NVR SFU 和 Meet 示例在 Pion PeerConnection 上启用 transport-wide RTP 头扩展发送器。对协商了该扩展的客户端，出站 RTP 获得逐包递增的传输序号；不支持该扩展的客户端不会获得此头扩展。Pion 的默认接收反馈拦截器仍负责处理 RTCP，NACK/PLI/FIR 路径保留。

验证：

```sh
go test -race ./cmd/lalmax-nvr -run 'TestNVRSFU(EmitsTransportCCAndReceivesFeedback|WorksWithoutTransportCCNegotiation|RecoversViewerPacketLossWithNACK)' -count=10
go test -race ./tools/sfu-meet -run TestDemoServerOpusThroughTwoPeers -count=10
```

本机 Pion 测试验证视频 RTP 扩展序号递增并收到 transport-cc RTCP 反馈，不协商扩展的接收端仍可收包，Meet Room 测试检查静音 Opus RTP 含扩展；测试不访问设备或播放声音。该改动只是采集传输反馈的基础：当前没有配置发送端带宽估计器，也没有按估计结果实施限速、pacing、选层或降级。公网、弱网和浏览器兼容性仍需验收。
