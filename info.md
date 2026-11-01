# lalmax-nvr for Home Assistant

Publish the streams you choose from an existing lalmax-nvr into Home Assistant.
HACS installs the HA integration only; run the NVR separately.

## Set up

1. Install this integration and restart Home Assistant (**2026.10.2 or newer**).
2. In the NVR, open **Settings → Home Assistant**, enable publication, choose streams and save.
3. In HA, open **Settings → Devices & services → Add integration → lalmax-nvr**. Enter the NVR address and HTTP account.
4. If RTSP authentication is enabled, enter its username and password separately. Add the resulting camera entities to your dashboard.

Live video, snapshots, availability, detection events and automation triggers are supported. Existing MP4 recordings appear in HA Media. Streams bound to ONVIF or GB28181 cameras can expose PTZ controls; movement requires an NVR super administrator.

H.264/AAC works with HLS. Optional low-latency WebRTC currently carries H.264 video; use HLS for AAC audio. HEVC playback depends on your device/browser. No automatic transcoding is started. Publishing a stream does not start recording.

See the [English setup and troubleshooting guide](https://github.com/lalmax-pro/lalmax-nvr/blob/main/docs/en/home-assistant.md).

## 中文说明

HACS 只安装 HA 集成，NVR 需单独运行。安装后重启 HA，在 NVR 的“设置 → Home Assistant”启用发布并选择流，再在 HA“设备与服务”中添加 **lalmax-nvr**。HTTP 与 RTSP 账号分别填写。

支持实时视频、缩略图、在线状态、检测事件、自动化、已有录像浏览及绑定设备的云台控制。H.264/AAC 可通过 HLS 播放；可选 WebRTC 当前提供 H.264 视频，AAC 声音请用 HLS。H.265 取决于终端支持，不会自动转码。发布流不会启动录像。

[中文安装与排障指南](https://github.com/lalmax-pro/lalmax-nvr/blob/main/docs/zh/home-assistant.md)
