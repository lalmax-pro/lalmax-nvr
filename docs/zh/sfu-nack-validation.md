# SFU Pion 出站 NACK 验证

`go test -race ./cmd/lalmax-nvr -run TestNVRSFURecoversViewerPacketLossWithNACK -count=30` 使用 NVR 的实际 SFU Pion PeerConnection 工厂及一个本机 Pion 接收端。接收端的 UDP socket 丢弃一次指定 RTP 包，丢弃发生在 SRTP 解密之前；随后测试检查协商出的通用 NACK、丢失序号的 RTCP 反馈及迟于后续包到达的重传包。

默认 Pion 拦截器负责缓存和重传；这里不把订阅者的 NACK 转发给原发布者。该测试只覆盖单个本机观看端的单包丢失，不经过 Room 分发、会议页面或桥接，不代表持续拥塞、多观看端、公网、突发丢包、关键帧恢复或生产容量已经验证。
