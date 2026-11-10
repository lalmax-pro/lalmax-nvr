# SFU 本机 TURN 媒体验证

运行：

```sh
go test -race ./cmd/lalmax-nvr -run TestNVRSFUTURNRelay -count=10
```

测试启动本机 UDP TURN 服务、使用 NVR SFU Pion 工厂创建发送端，并要求 Pion 观看端只使用 TURN relay 候选。协商后检查选中的本地候选确为 relay，验证 data channel 可用，并验证一包合成 H.264 RTP 经 TURN 到达观看端。无需摄像头、麦克风、屏幕或扬声器。

此测试只覆盖本机 IPv4 UDP TURN 和单个 Pion 接收端；它不经过 Room、NVR 信令或浏览器，也不覆盖公网 NAT、防火墙、TCP/TLS TURN、真实视频解码或浏览器兼容性。
