# SFU 桥接录像：合成媒体验证

以下可选测试需要本机安装带 `libx264`、`libopus` 的 FFmpeg 和 ffprobe；测试生成合成画面及音频，离线解码，不打开摄像头、麦克风、屏幕或扬声器。

```sh
SFU_DECODE_RECORDING_TEST=1 go test -race ./internal/recorder -run '^TestSFUDecodableBridgeRecording' -count=3
SFU_AV_SYNC_TEST=1 go test -race ./internal/recorder -run '^TestSFUBridgeRecordingKeepsEncodedAVMarkersAligned$' -count=3
SFU_AV_LONG_TEST=1 go test -race ./internal/recorder -run '^TestSFULongBridgeRecordingKeepsEncodedAVMarkersAligned$' -count=1
SFU_NVR_RECORDING_TEST=1 go test -race ./cmd/lalmax-nvr -run '^TestNVRSFUBridgeRecordingPlanEndToEnd$' -count=1
SFU_NVR_RECORDING_TEST=1 SFU_NVR_RECORDING_LONG_TEST=1 go test -race ./cmd/lalmax-nvr -run '^TestNVRSFUBridgeRecordingPlanEndToEnd$' -count=1
SFU_NVR_RECORDING_TEST=1 SFU_NVR_RECORDING_MARKERS_TEST=1 go test -race ./cmd/lalmax-nvr -run '^TestNVRSFUBridgeRecordingPlanEndToEnd$' -count=5
```

第一组测试生成 H.264 画面和 Opus 静音，按 RTP 送入 SFU 音视频桥接，再经 lal 重封装、真实 lalmax Group、帧订阅和录像器生成 MP4。测试覆盖一秒连续音视频，以及四秒视频中 2.4 秒的音频间隙，并由 FFmpeg 离线解码。Opus 样本表中包含一个用于解码预热的合成静音包；它会被 edit list 跳过，不应算作源音频包。可通过 `SFU_DECODE_RECORDING_MP4=/绝对路径/输出.mp4` 保留产物；同时运行多个用例时应避免使用同一输出路径。

第二组测试在源视频和 Opus 音频的相同时间放置标记，离线解码后比较时间；覆盖同源、音频领先或落后 200 毫秒及接近实时速率。视频解码使用 passthrough 模式，检查全部帧，避免默认补帧掩盖丢帧。第三组以接近实时速率运行约一分钟，音频设定晚 200 毫秒；一次本机运行中录像器收到 3000 个音频包，解码得到 1500 帧视频，视频标记为 0.48、30、59 秒，音频标记为 0.68、30.2、59.2 秒。

前三组使用测试发布适配器。第四组启动本机 NVR，经受保护的票据和信令发布合成视频与静音音频，再调用桥接和录像计划 API，检查录像计划在视频编码信息稍后就绪时能自动启动，最终用 ffprobe 检查音视频包数并用 FFmpeg 离线解码 MP4。第五组在同一 NVR 链路中接近实时运行一分钟，以 2 分钟录像分段避免测试中途切段，检查音视频包数及轨道时长差；它不验证标记对齐或听感。第六组用合成闪屏和离线生成的音频信号比较两次时间标记；使用视频帧时间戳及离线解码的音频，整个过程不使用扬声器。可通过 `SFU_NVR_RECORDING_MP4=/绝对路径/输出.mp4` 保留产物；测试结束时会向测试进程发送 SIGTERM 以关闭 NVR，不应与其他依赖进程信号的测试同时运行。

这些测试不能代替真实设备、真实网络、其他播放器、人工听感和更长时间同步验收。不同 FFmpeg 构建需要提供上述编码器。
