# SFU Meet local signaling server

This is an early local development endpoint for [issue #93](https://github.com/lalmax-pro/lalmax-nvr/issues/93). From the repository root:

```sh
go run ./tools/sfu-meet
```

The process listens on an ephemeral `127.0.0.1` port and prints its exact HTTP origin and a randomly generated access token. The token is valid only until restart. Send a same-origin `POST /session` with `{"token":"..."}` to obtain an HttpOnly, SameSite=Strict session cookie. Connect to `/ws` from that exact origin with the cookie. A `GET /healthz` returns 204. All authorized connections join a single in-memory `local/demo` room. Never expose this development server to a network or reuse its shared token for production.

The existing [signaling protocol](../../third/lalmax/sfu/signaling/README.md) describes control messages. This first server accepts Opus microphone audio only. Clients must declare their WebRTC Track ID with `publish` before their initial fully gathered SDP offer; viewers subscribe to existing publications before their initial offer. Only one offer is supported per connection. Peers that join or change subscriptions later must reconnect and renegotiate; trickle ICE, dynamic tracks, automatic keyframe feedback, active permission revocation and persistent rooms are not implemented.

There is currently no browser UI; `GET /` identifies the running service. A future `tools/sfu-meet` frontend will use this endpoint. The service is not integrated into the NVR or lalmax production server and does not provide GroupBridge or recording.
