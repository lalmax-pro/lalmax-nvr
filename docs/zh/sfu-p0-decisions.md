# SFU P0 首版决策与实现边界

本文记录 [Issue #93](https://github.com/lalmax-pro/lalmax-nvr/issues/93) 已采用的首版边界及明确延期事项。P0 的“确定”表示决策可检查，不表示延期功能已经实现或达到生产验收。

## 对象和生命周期

- Room 由 `(namespace, roomID)` 标识，每个内存实例另有随机生成的 generation；Participant 会话、Publication 和 Subscription 均属于具体 Room。Track 发布 ID 由服务端分配；旧成员会话不能操作或清理新会话。参见 `third/lalmax/sfu/room.go`、`tracks.go` 和 `access/session.go`。
- Room 是媒体发现、订阅和授权边界，不自动创建 lal Group，也不混音或混画。GroupBridge 是经授权的系统订阅者，单轨或同一发布者的音视频可输出至独立 Group；跨房间或跨发布者组合不允许。桥接目标由服务端生成，桥接输出身份持久化为拒绝回退的历史记录。
- Room 关闭、会话结束、Track 撤销会结束关联订阅和媒体绑定；桥接也会停止并撤销播放授权。关闭与重建之间必须校验 generation。NVR 仅单节点、内存 Room：重启不恢复动态房间、Track、订阅或桥接期望。
- NVR 管理员显式创建房间；`default` 为兼容房间且不可通过管理 API 关闭。额外房间最多 31 个；每房间限制 32 成员、每成员 3 发布、256 订阅、32 观察者，最多 8 个并发桥接。`sfu.idle_timeout` 留空时不自动回收；配置正时长后，非默认空闲房间按宽限期回收，有成员（包括桥接订阅者）时不回收。
- **重连边界：** 不提供恢复凭证或服务端会话续接。Meet 页面在信令断开或媒体连接失败后最多重试三次，重新认证、获取新票据及快照，重新建立 PeerConnection/发布/订阅并校验房间 generation；保留仍存活的摄像头、麦克风及其静音状态。屏幕共享和桥接会停止，需用户重新启动。弱网和长时间重连效果仍需 P2 验证。

## 信令、授权、端口与编解码

- 可嵌入的 `signaling.Handler` 要求调用方提供受信身份、Room 管理器、授权器、可见性过滤器和 Origin 校验；信令不会根据未授权房间号自动创建 Room。WebSocket 命令为 `publish`、`unpublish`、`subscribe`、`unsubscribe` 和 `offer`，返回结果或过滤后的房间快照；参见 `third/lalmax/sfu/signaling/README.md`。
- `access.Authorizer` 按 Room generation、成员、操作和目标检查 join/publish/unpublish/subscribe/unsubscribe/manage_bridge；原始 Room API 不代替鉴权。NVR 当前仅向启用的超级管理员或符合条件的旧版管理员开放会议和房间管理，并定期复核账号；独立嵌入者负责自己的认证、可见性和撤权。更细的房间 ACL、观看者/发布者角色和产品策略未实现。
- NVR 管理接口运行在配置的 HTTP 服务上，默认 `server.listen` 为 `:9090`；`/api/sfu/ticket` 签发 30 秒一次性房间票据，`/api/sfu/ws` 使用该票据。可选 generation 防止旧房间选择误入同名新房间；管理和信令应部署在可信网络或 HTTPS 下。
- SFU 的 Pion PeerConnection 使用独立 ICE 配置 `sfu.ice_servers`，不承诺固定共享媒体端口；Group 的 WHEP 使用媒体服务自身的 ICE/监听配置，两者不可混同。NVR 仅支持内嵌媒体引擎；公网/NAT/TURN 的具体部署与连通性仍需验收。
- NVR 首版协商 Opus 音频和 H.264 视频；桥接视频额外检查 negotiated H.264 profile/packetization 与码流约束，音频要求 Opus。屏幕共享当前只发布视频；VP8/VP9/AV1、H.265、音频转码和屏幕音频不在此首版承诺内。

## 媒体所有权、反馈与隔离

- 入站 RTP 在有界 fanout 中为每个订阅者复制；调用者在写入期间不得并发修改输入包。慢订阅者队列满时仅断开该订阅，写入不等待慢消费者。桥接运行器拥有其订阅，sink 如需在返回后保留数据须自行复制。
- 视频 PLI/FIR 以房间代次和 publication 为范围映射、合并与限频；NVR 还定时请求视频关键帧。Pion 默认拦截器不代表完整拥塞闭环；重传/NACK 的业务策略、带宽估计、pacing、Simulcast、弱网和容量验证仍在 P2/P3。
- 桥接使用独立订阅和有界队列，封装、写入或目标失败只终止该桥接，不能重发源 Track 或主动断开其他订阅者。异步慢 sink 的清理可受阻，关闭上下文限定等待时间，超时后继续追踪待清理注册表并重试。单轨音频时间戳与组合音视频同步能力不同；长时同步、录制和人工听音仍待验收。

## 与验收的关系

上述内容固定当前首版的接口和运行边界；它不承诺细粒度授权、跨节点、服务端会话续接、生产容量或完整弱网能力。Issue 中 P1 的实际链路和 P2/P3 的专项验收仍分别跟踪。
