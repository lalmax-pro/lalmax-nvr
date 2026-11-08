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

To use the same page through NVR, enable `sfu.backend: livekit` as shown in
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

The LiveKit page does not yet provide Group bridge management. See issue #93
for remaining integration and acceptance work. The bundled SDK version and
license are recorded in `internal/sfumeetui/LIVEKIT-SDK.md`.

For the optional browser integration test, install the test-only dependency and
point to an installed Chrome or Chromium executable:

```sh
npm ci --prefix tools/sfu-meet
LIVEKIT_BROWSER_EXECUTABLE=/path/to/chrome go test -tags livekit_browser ./cmd/lalmax-nvr -run '^TestLiveKitBrowser$' -count=1
```

The test uses Chrome's fake media devices, mutes browser audio, and supplies a
canvas and isolated Web Audio stream for screen sharing. It does not capture
real devices or the desktop.
