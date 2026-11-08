# SFU 桥接录像：可解码媒体验证

执行需要本机安装带 libx264 的 FFmpeg 和 ffprobe，默认测试不会执行该用例：

```sh
SFU_DECODE_RECORDING_TEST=1 go test -race ./internal/recorder -run TestSFUDecodableBridgeRecording -count=10
```

用例用 FFmpeg 的 testsrc2 生成一秒、25 fps、160×96 的 H.264，无需摄像头；Opus 使用合成静音包，无需麦克风。H.264 按 1200 字节上限打包成 RTP，包括 FU-A 分片，再经进程内 SFU 音视频桥接、lal 重封装、真实 lalmax Group、帧订阅及录像器生成 MP4。`ffprobe` 检查 25 帧和视频分辨率，FFmpeg 将音视频解码到 null，不连接扬声器或显示器。

本机 FFmpeg 验证得到 25 个视频帧和 50 个 Opus 包，两个轨道媒体时长均为 1 秒；音频轨道的可见起始时间为 80 ms，来自封装器的 Opus pre-skip。可通过 `SFU_DECODE_RECORDING_MP4=/绝对路径/输出.mp4` 保留产物以便分析。

该验证使用测试发布适配器而非真实 NVR 发布入口，编码画面为合成视频，音频没有语音；未覆盖真实设备、真实网络、长时间运行或人工听感，也不能由轨道时长推断长期音画同步。不同 FFmpeg 构建需要提供 `libx264`。
