# SFU Room 下行单包丢失验证

运行：

```sh
go test -race ./tools/sfu-meet -run TestRoomNACKRecovers -count=20
```

测试启动本机 Meet 信令服务，一个 Pion 发布者和两个 Pion 观看端。视频 RTP 经真实 Room、发布和订阅分发；在其中一个观看端 UDP socket 分别丢弃单包或连续 10 个 RTP 包，检查该端恢复丢失序号，同时另一观看端收到对应的连续包。测试不访问摄像头、麦克风、屏幕或扬声器。

测试的丢包发生在接收端 SRTP 解密之前，以便模拟真实网络丢包，而不是在 SRTP 防重放检查之后丢包。单个 Pion 出站连接的 NACK 反馈及重传验证另见 `sfu-nack-validation.md`。

这是本机的一次短突发丢包实验，不代表反复突发丢包、持续拥塞、公网、长时间运行、浏览器解码或关键帧恢复已验收。它使用 Meet 示例服务，不代表已通过 NVR 真实入口验证。
