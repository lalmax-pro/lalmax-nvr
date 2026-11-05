# SFU Meet local signaling server

This is an early local development endpoint for [issue #93](https://github.com/lalmax-pro/lalmax-nvr/issues/93). From the repository root:

```sh
go run ./tools/sfu-meet
```

Open the printed `127.0.0.1` address in a browser and enter the randomly generated access token from the terminal. Enable the microphone or join in listen-only mode. The token is valid only until restart. The page exchanges it via same-origin `POST /session` for an HttpOnly, SameSite=Strict session cookie, then connects to `/ws`. A `GET /healthz` returns 204. All authorized connections join a single in-memory `local/demo` room. Never expose this development server to a network or reuse its shared token for production.

The existing [signaling protocol](../../third/lalmax/sfu/signaling/README.md) describes control messages. This first server accepts Opus microphone audio only. Clients must declare their WebRTC Track ID with `publish` before their initial fully gathered SDP offer; viewers subscribe to existing publications before their initial offer. Only one offer is supported per connection. Peers that join or change subscriptions later must reconnect and renegotiate; trickle ICE, dynamic tracks, automatic keyframe feedback, active permission revocation and persistent rooms are not implemented.

The embedded browser UI supports joining, microphone mute, remote audio and room membership. When published tracks change, use the page’s reconnect action to update subscriptions. The service is not integrated into the NVR or lalmax production server and does not provide GroupBridge or recording.
