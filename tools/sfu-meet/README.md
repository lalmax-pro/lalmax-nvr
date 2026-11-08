# SFU Meet

The Meet demo uses the embedded LiveKit server and the locally bundled
LiveKit browser SDK. To run the isolated local demo from the repository root:

```sh
go run ./tools/sfu-meet
```

Open the printed `127.0.0.1` address and enter the access token shown in the
terminal. The local server binds only to loopback and creates a temporary
`demo` room. Its token and LiveKit keys change on each start; do not expose
the local demo to other networks.

To use the same page through NVR, enable SFU as shown in
`config/config.example.yaml`, configure the native LiveKit file using
`config/livekit.example.yaml`, then open `/sfu-meet/` on the NVR server and
sign in as an administrator. The configured LiveKit public URL and media
ports must be reachable from browsers. Use HTTPS/WSS outside a trusted local
setup; camera and microphone access also requires a secure browser context.

Microphone and camera start disabled. Participants can join as viewers,
enable or switch devices, publish screen video and optionally screen audio,
and subscribe or unsubscribe from individual remote tracks. Screen audio
availability depends on the browser and selected source. If autoplay is
blocked, select “启用声音”. LiveKit handles transient connection recovery.

In NVR, administrators can create and close rooms, select tracks to bridge to
a server-assigned lal Group, and play, stop or remove their bridges. Combined
audio and video must belong to the same participant and source (camera and
microphone, or screen and screen audio). The bridge accepts H.264 constrained
baseline video and 48 kHz Opus audio. Group playback starts muted; unmute it
explicitly in the player. A bridge ends when its source or room ends. Recording
plans can target the generated stream ID. Group bridging requires the embedded
lalmax media engine; pure SFU meetings also work with an external media engine.

The previous SFU backend has been removed. When migrating an enabled SFU,
replace its old `idle_timeout`, `ice_servers`, `observe_bandwidth` and `pacing`
settings with the appropriate native LiveKit configuration; an enabled legacy
backend or those old settings produce an explicit configuration error. Existing
legacy signaling clients must use the LiveKit SDK and join endpoint.

The bundled SDK version and license are recorded in
`internal/sfumeetui/LIVEKIT-SDK.md`.

For the optional browser integration test, install the test-only dependency and
point to an installed Chrome or Chromium executable:

```sh
npm ci --prefix tools/sfu-meet
LIVEKIT_BROWSER_EXECUTABLE=/path/to/chrome go test -tags livekit_browser ./cmd/lalmax-nvr -run '^TestLiveKitBrowser$' -count=1
```

To exercise the full NVR recording plan and decode a synthetic recording,
run `TestNVRLiveKitRecordingBrowser` with the same build tag and executable.
With FFmpeg installed, check generated audio/video synchronization markers with:

```sh
SFU_NVR_RECORDING_TEST=1 SFU_NVR_RECORDING_MARKERS_TEST=1 go test ./cmd/lalmax-nvr -run '^TestNVRSFUBridgeRecordingPlanEndToEnd$'
```

The test uses Chrome's fake media devices, mutes browser audio, and supplies a
canvas and isolated Web Audio stream for screen sharing. It does not capture
real devices or the desktop.
