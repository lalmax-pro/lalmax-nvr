# SFU 桥接录像时间轴边界

MP4 封装器为多条轨道选择最早的首个解码时间作为公共起点，并用 edit list 表示其他轨道的首包延迟。Opus 轨道的空编辑还包含现有的 80 ms 解码预跳过；音视频先后顺序都可表达。轨道起始时间必须来自同一个时间基准，不同来源的时钟不能直接比较。

可复现的合成样本：

```sh
SFU_OFFSET_TEST_MP4=/tmp/sfu-offset.mp4 go test ./internal/muxer -run '^TestMP4TrackEditPreservesLateOpusOffset$' -count=1
ffprobe -v quiet -show_entries stream=codec_name,start_time,duration -of json /tmp/sfu-offset.mp4
```

样本的视频和音频输入首包分别位于同一时钟的 1000 ms 与 1200 ms。`ffprobe` 识别视频起始 0 ms、Opus 起始 280 ms，其中 200 ms 为轨道延迟，80 ms 为预跳过。样本视频 NAL 仅用于结构测试，不应作为真实视频播放验证。

已登记的 SFU 桥接输出在 H.264 进程内录像路径中使用源视频时间戳和同一来源的音频时间戳；其他流及 RTSP 回退保持原有墙钟行为。暂停后的预录帧不含源时间，包含预录帧的当前片段继续使用墙钟；下一片段可恢复源时间。突发输入的合成测试中，20 ms 媒体间隔以 3 ms 实际间隔送入，21 帧视频覆盖 0.420 s（末帧沿用前一帧间隔），20 包音频的媒体时长为 0.400 s，音频源时间从 20 ms 开始。

对使用源时间的 Opus 音轨，录像器会为不超过 2 秒且与 20 ms 对齐（允许 1 ms 舍入误差）的中途间隙插入 20 ms 静音包，使用 [Pion WebRTC 测试中的静音包](https://github.com/pion/webrtc/blob/v4.2.23/e2e/e2e_test.go)。静音只能保留时间轴，不能还原丢失的声音。超过上限或无法对齐的间隙会记录警告，仍可能造成漂移；不会影响会议转发。合成 RTP 的短间隙测试已用 FFmpeg 静默解码，真实设备听感、其他播放器兼容性、弱网和长时间同步仍待验收。
