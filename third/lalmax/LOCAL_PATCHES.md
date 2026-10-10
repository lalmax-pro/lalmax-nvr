# Local patches

Upstream: git@github.com:q191201771/lalmax.git
Pinned commit: ffdfe24e1a84d1b97e4ed50a527833e4f7efb283

This file records local changes made to `third/lalmax` for lalmax-nvr.

## Current patches

### embed-lifecycle

- Reason: lalmax-nvr embeds lalmax as an in-process media engine and needs explicit start, readiness and shutdown hooks.
- Files:
  - `server/server.go`
  - `srt/server.go`
  - `rtc/server.go`
  - `gb28181/rtppub/manager.go`
  - `server/zlm_compat_ffmpeg.go`
- Upstreamable: Yes, as a generic embedded lifecycle API.
- Test: `go test ./server ./srt ./rtc ./gb28181/rtppub`

### udp-ts-customize-pub

- Reason: Pull UDP (unicast/multicast) MPEG-TS into lalmax via CustomizePub. Demux uses go-astits so PAT/PMT program_number can be selected (URL `?program=` or JSON `program_id`); gomedia OnFrame has no program id. AAC still uses gomedia go-codec like SRT.
- Files:
  - `udpts/`
  - `server/server.go`
  - `server/router_ctrl.go`
  - `server/router_zlm_compat.go`
- Upstreamable: Yes.
- Test: `go test ./udpts ./server`

### voip-media

- Reason: NVR SIP inbound calls need RTP/SRTP receive, SDP negotiation and publish sessions without signaling dependencies inside lalmax.
- Files: `voip/`.
- Supported codecs: Opus, AAC, PCMA/PCMU, H264/H265; AV1 is excluded because lal does not support it.
- Test: `go test -race -count=1 ./voip/...`

### pion-dependencies

- Reason: Keep the standalone lalmax module and the NVR module on the same Pion versions for WebRTC and VoIP media.
- Files: `go.mod`, `go.sum`, `rtc/peerConnection_test.go`.
- Versions: WebRTC v4.2.23, ICE v4.4.7, DTLS v3.1.10, SRTP v3.1.3, RTP v1.10.5; related Pion dependencies are updated together.
- Test: `go test -race -count=1 ./rtc ./voip/...`; the RTC test receives real RTP over DTLS/SRTP and verifies that the shared ICE UDP port can be reused after a call closes.

## Patch template

### patch-name

- Reason:
- Files:
- Upstreamable:
- Test:

### embedded-config-and-hook-snapshots

Detach global/server configuration snapshots and synchronize dynamic hook configuration/client replacement with dispatch. Files: `config/`, `server/`. Test: `go test -race -count=1 ./config ./server`.

### voip-media-renegotiation

- Reason: Preserve verified DTLS-SRTP keys/replay counters across track changes; suppress held media and defer held-track timeouts.
- Files: `voip/pub_session.go`, `voip/media/`, `voip/sdp/`.
- Test: `go test -race -count=1 ./voip/...`; real Linphone audio-to-video upgrade and FFmpeg decode.

### voip-web-duplex-audio

- Reason: Bridge browser WebRTC microphone and terminal RTP/SRTP in both directions with the selected audio codec, shared RTC ICE configuration and independent sender SSRC/sequence numbers.
- Files: `rtc/talksession.go`, `server/server.go`, `voip/pub_session.go`, `voip/media/`, `voip/sdp/talk.go`.
- Supported codecs: Opus, PCMA, PCMU; terminal media supports RTP, SDES-SRTP and active/passive DTLS-SRTP.
- Test: `go test -race -count=1 ./rtc ./server ./voip/...`; NVR `TestOutboundSecureMediaAndCodecs` covers both audio directions for all codec/security combinations.
