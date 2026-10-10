# Local patches

Upstream: git@github.com:q191201771/lal.git
Pinned commit: 8e5eb173ec80f806ed37cd9d02177a1e1e567206

This file records local changes made to `third/lal` for lalmax-nvr.

## Current patches

### customize-publisher-statistics

- Reason: Audio-only VoIP/custom publishers were reported as inactive because their session statistics had no ID or protocol. Use the same session ID for statistics and publisher control.
- Files: `pkg/base/basic_session_stat.go`, `pkg/base/basic_session_stat_test.go`, `pkg/logic/customize_pubsession.go`, `pkg/logic/customize_pubsession_test.go`.
- Upstreamable: Yes.
- Test: `go test ./pkg/base ./pkg/logic`; NVR's audio-only custom publisher stream-list regression also covers the statistics API mapping.

## Patch template

### patch-name

- Reason:
- Files:
- Upstreamable:
- Test:

### embedded-concurrent-lifecycle

Serialize global/prefixed logger initialization with writes and synchronize HTTP listener admission/close. Keep runtime log changes while preventing races during media restarts. Files: `pkg/base/logger.go`, `pkg/base/http_server.go` and regression tests. Test: `go test -race -count=1 ./pkg/base ./pkg/logic`.

### customize-publisher-track-reset

- Reason: Refresh SDP/remux tracks and GOP caches when a VoIP call changes codecs or adds/removes video while retaining its stream name.
- Files: `pkg/logic/customize_pubsession.go`, `pkg/logic/group__in.go`.
- Test: `go test -race -count=1 ./pkg/logic ./pkg/base`; NVR media replacement regression and Linphone video upgrade.
