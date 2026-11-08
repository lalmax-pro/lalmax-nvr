# NVR SFU 会议（实验功能）

NVR 在进程内运行 LiveKit。按 `config/config.example.yaml` 启用 `sfu`，并参考 `config/livekit.example.yaml` 配置 LiveKit 的密钥、房间及网络参数；重启后访问 `/sfu-meet/`，使用 NVR 超级管理员账号加入。浏览器使用摄像头、麦克风或屏幕需要 HTTPS 或 localhost 安全上下文及相应权限。独立本机演示的启动方式见 `tools/sfu-meet/README.md`。

会议采用 LiveKit 的房间、成员和 Track，支持仅观看、多轨发布和按轨道订阅。NVR 签发有效期五分钟、限定房间的 LiveKit 凭证；账号被撤权后不能再签发新凭证，但已签发的凭证仍可能在有效期内使用。LiveKit 自身负责连接恢复及媒体转发。公网地址、STUN/TURN、房间空闲时间和其他媒体参数在原生 LiveKit 配置中设置；lalmax 的 WHEP 播放使用单独的 ICE 配置。

管理员可通过 Meet 页面或受保护的 API 管理临时房间：

- `GET /api/sfu/rooms/` 列出房间 ID、运行代次和可见成员数。
- `POST /api/sfu/rooms/` 接收 `{"id":"room1"}` 创建房间。
- `DELETE /api/sfu/rooms/room1?generation=<代次>` 关闭指定代次；默认房间不能由此接口关闭。
- `POST /api/sfu/livekit/join` 接收 `{"room":"room1"}`，返回 LiveKit 地址及短期入会凭证。入会也可以按 LiveKit 的房间创建策略创建房间。

房间 ID 仅允许 1–64 个英文字母、数字、下划线或连字符。连接、Track 和桥接属于运行时状态，不保证重启后恢复；房间持久性和空闲回收以 LiveKit 配置为准。

## 可选 Group 桥接

纯 SFU 会议不创建 lal Group。管理员可以在 Meet 页面选择 Track 并发布到服务端生成的独立 Group，在页面内通过 WHEP 播放，或通过 NVR 录像计划录制生成的流。播放默认静音，需要时由用户手动打开声音。使用外部媒体引擎时可运行纯 SFU，Group 桥接需要内嵌 lalmax 媒体引擎。

桥接支持一条 H.264 constrained baseline 视频、一条 48 kHz Opus 音频，或同一成员、同一来源的音视频组合（摄像头与麦克风，或屏幕与屏幕音频）。组合桥接依赖发布端 RTCP Sender Report 建立公共时钟。发布结束、房间关闭或管理员撤权会停止相应桥接；重启后不会自动恢复。持久化的输出身份用于防止旧桥接流绕过播放授权。媒体引擎实例被替换后，需要重启 NVR 才能重新建立桥接。

主要管理接口均要求管理员认证：

- `POST /api/sfu/bridges/` 接收 `room_id`、`room_generation`、`publication_id`，以及可选的 `audio_publication_id`。请求可带 `Idempotency-Key`，用于在创建响应丢失时避免重复创建。
- `GET /api/sfu/bridges/` 列出当前管理员拥有的桥接；`POST /api/sfu/bridges/<id>/stop` 停止，终态后 `DELETE /api/sfu/bridges/<id>` 移除。
- `GET /api/sfu/bridge-requests` 使用相同的 `Idempotency-Key` 查询创建结果；结果只在当前进程内保留，已清理的桥接返回 404。
- `POST /api/sfu/bridges/<id>/whep` 接收 SDP offer，返回 SDP answer 和用于删除播放会话的 `Location`。音视频尚未就绪时返回 503，可稍后重试。

旧自研 SFU 的 `backend: legacy`、`sfu.ice_servers`、`idle_timeout`、`observe_bandwidth` 和 `pacing` 不再用于启用的 SFU；迁移时请改用 LiveKit 配置。旧的 `/api/sfu/ticket` 和 `/api/sfu/ws` 已移除，客户端需要使用 LiveKit SDK。

本机已用合成媒体验证播放和录像解码；真实设备、跨 NAT、弱网、长期运行及生产容量仍需按实际环境验收。
