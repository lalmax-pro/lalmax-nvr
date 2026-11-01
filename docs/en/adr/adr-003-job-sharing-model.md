# ADR-003: Same source+profile transcode jobs share a physical output

- **Status**: Accepted
- **Date**: 2026-10
- **Related**: Issue #66, [Transcoding P0 Decisions](../transcoding-p0-decisions.md)

## Context

Multiple consumers (NVR web UI, Home Assistant, external players) may simultaneously need the same camera stream at the same encoding specification. The question was whether each consumer should launch an independent transcode process, or share a single physical output.

## Alternatives

**Option A: Each Job gets its own process**
- Simplest implementation, independent lifecycle
- Tradeoff: N consumers = N transcode processes; CPU / memory grow linearly — unacceptable when multiple services consume the same camera

**Option B: Same WorkKey shares a physical output**
- WorkKey = `sha256(engineID + appName + streamID + effectiveProfileHash)`
- Multiple Jobs with the same WorkKey share one physical output and one backend process
- Stopping one consumer does not affect others (reference counting)

## Decision

**Option B**: Jobs sharing a physical output.

`EffectiveHash` deliberately excludes the Profile's `Name` (display name) and `Revision` (version number), covering only media parameters (backend, video, audio specs). This ensures functionally equivalent configurations share the same physical output.

Sharing rules:
- On a WorkKey hit, the new Job references the existing physical output — no new process is started
- Stopping a Job releases only that Job's reference; the physical process keeps running while other Jobs still hold references
- The physical process is stopped and authorization revoked when the last Job releases

## Tradeoffs and Known Limitations

- Stop semantics need to be explicit: stopping a single Job vs. globally disabling an output are different operations
- Per-source ACL (permission inheritance for different consumers) is not yet implemented (P3 delivery gate)
- Version watermarks (generation) protect against old-version Jobs overwriting new task state, but unbounded watermark growth needs to be addressed in P1
