# Architecture Decision Records (ADR)

This directory records major architecture decisions for lalmax-nvr. Each record describes the context, alternatives considered, final choice, and known tradeoffs.

> ADRs are decision snapshots and are not updated as implementation evolves. If a decision is reversed, create a new "Superseded" record and cross-reference the old one.

---

## Index

| # | Title | Status | Date |
|---|-------|--------|------|
| [ADR-001](adr-001-embedded-media-engine.md) | Embedded media engine as default mode | Accepted | 2026-10 |
| [ADR-002](adr-002-dual-transcode-backend.md) | Dual transcode backends (FFmpeg / GStreamer) with shared Manager | Accepted | 2026-10 |
| [ADR-003](adr-003-job-sharing-model.md) | Same source+profile transcode jobs share a physical output | Accepted | 2026-10 |
| [ADR-004](adr-004-cgo-free.md) | Keep CGO_ENABLED=0; run media tools as subprocesses | Accepted | 2026-10 |
| [ADR-005](adr-005-sqlite-default-db.md) | SQLite as default database with PostgreSQL / MySQL switchover | Accepted | 2026-10 |

---

## Status Definitions

| Status | Meaning |
|--------|---------|
| Proposed | Raised, not yet confirmed |
| Accepted | Confirmed and implemented |
| Superseded | Replaced by a newer decision; see cross-reference |
| Deprecated | Feature deprecated |

---

## Adding a New Record

Name new ADR files `adr-NNN-<kebab-title>.md` and use this template:

```markdown
# ADR-NNN: Title

- **Status**: Proposed / Accepted
- **Date**: YYYY-MM-DD
- **Related**: Issue #xx, PR #xx

## Context

Describe the problem or constraint that requires a decision.

## Alternatives

List options considered with their pros and cons.

## Decision

Which option was chosen and why.

## Tradeoffs and Known Limitations

The cost, constraints, or follow-up work this decision introduces.
```
