# SFU Meet local signaling server

This is an early local development endpoint for [issue #93](https://github.com/lalmax-pro/lalmax-nvr/issues/93). From the repository root:

```sh
go run ./tools/sfu-meet
```

Open the printed `127.0.0.1` address in a browser and enter the randomly generated access token from the terminal. Choose microphone and camera before joining; participants can also join without publishing media. The token lasts until restart and is exchanged via same-origin `POST /session` for an HttpOnly, SameSite=Strict session cookie. All authorized connections join one in-memory `local/demo` room. Never expose this development server to a network or reuse its shared token for production.

The UI supports microphone mute, camera mute, screen sharing, remote audio/video and automatic subscription changes as room tracks appear or disappear. Video uses H.264 constrained baseline with packetization mode 1; a compatible browser is required. Screen sharing requires browser permission and user selection. Browser autoplay policies may require the user to enable sound. Screen audio is not published. Up to three tracks per participant allow microphone, camera and screen simultaneously.

The demo periodically requests upstream video keyframes to support late subscribers; this is a local-demo fallback, not subscriber RTCP feedback routing. The existing [signaling protocol](../../third/lalmax/sfu/signaling/README.md) describes its control messages and SDP renegotiation. Trickle ICE, selective subscription controls, active permission revocation and persistent rooms are not implemented. The service is not integrated into the NVR or lalmax production server and does not provide GroupBridge or recording.
