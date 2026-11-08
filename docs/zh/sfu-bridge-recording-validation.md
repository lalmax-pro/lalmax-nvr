# SFU 桥接录像：合成媒体验证

以下可选测试需要本机安装带 `libx264`、`libopus` 的 FFmpeg 和 ffprobe；测试生成合成画面及音频，离线解码，不打开摄像头、麦克风、屏幕或扬声器。

```sh
SFU_DECODE_RECORDING_TEST=1 go test -race ./internal/recorder -run '^TestSFUDecodableBridgeRecording' -count=3
SFU_AV_SYNC_TEST=1 go test -race ./internal/recorder -run '^TestSFUBridgeRecordingKeepsEncodedAVMarkersAligned$' -count=3
SFU_AV_LONG_TEST=1 go test -race ./internal/recorder -run '^TestSFULongBridgeRecordingKeepsEncodedAVMarkersAligned$' -count=1
```

第一组测试生成 H.264 画面和 Opus 静音，按 RTP 送入 SFU 音视频桥接，再经 lal 重封装、真实 lalmax Group、帧订阅和录像器生成 MP4。测试覆盖一秒连续音视频，以及四秒视频中 2.4 秒的音频间隙，并由 FFmpeg 离线解码。Opus 样本表中包含一个用于解码预热的合成静音包；它会被 edit list 跳过，不应算作源音频包。可通过 `SFU_DECODE_RECORDING_MP4=/绝对路径/输出.mp4` 保留产物；同时运行多个用例时应避免使用同一输出路径。

第二组测试在源视频和 Opus 音频的相同时间放置标记，离线解码后比较时间；覆盖同源、音频领先或落后 200 毫秒及接近实时速率。视频解码使用 passthrough 模式，检查全部帧，避免默认补帧掩盖丢帧。第三组以接近实时速率运行约一分钟，音频设定晚 200 毫秒；一次本机运行中录像器收到 3000 个音频包，解码得到 1500 帧视频，视频标记为 0.48、30、59 秒，音频标记为 0.68、30.2、59.2 秒。

这些用例使用测试发布适配器，不能代替从真实 NVR 发布入口到录像计划的端到端验证。真实设备、真实网络、其他播放器、人工听感和更长时间同步仍待验收。不同 FFmpeg 构建需要提供上述编码器。
