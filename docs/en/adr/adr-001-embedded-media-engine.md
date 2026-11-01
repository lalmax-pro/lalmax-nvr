# ADR-001: Embedded media engine as default mode

- **Status**: Accepted
- **Date**: 2026-10
- **Related**: Architecture design, see [architecture.md](../architecture.md)

## Context

lalmax-nvr depends on the lalmax / lal media engine for stream ingest, protocol transcoding, and distribution. The engine can run in the same process as the NVR (embedded) or as a standalone process connected via HTTP (http mode). A default recommendation was needed.

## Alternatives

**Option A: embedded mode (same process)**
- Go code calls lalmax API directly
- No network overhead, in-process communication
- Single-binary deployment, no extra process management
- Tradeoff: NVR and media engine share a lifecycle — a crash in one affects the other

**Option B: http mode (separate process)**
- NVR communicates with an external lalmax via HTTP API
- Media engine can be upgraded independently
- Tradeoff: requires additional deployment and port configuration, higher latency, more operational complexity

## Decision

**Embedded mode** is the default.

Reasoning:
1. Target users (home / small-business NVR) prioritize single-binary simple deployment
2. Embedded mode has better performance — no HTTP serialization overhead
3. http mode is retained as an advanced option for scenarios requiring an independent media engine

## Tradeoffs and Known Limitations

- MJPEG / HTTP JPEG sources are pulled directly by the NVR process (lalmax does not handle these)
- ONVIF multicast discovery (WS-Discovery) does not work in Docker bridge networking — requires `network_mode: host`
- In embedded mode, media engine ports (12090, 4888, etc.) are owned by the NVR process and must be exposed together
