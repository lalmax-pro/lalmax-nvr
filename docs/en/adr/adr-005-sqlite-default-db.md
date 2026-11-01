# ADR-005: SQLite as default database with PostgreSQL / MySQL switchover

- **Status**: Accepted
- **Date**: 2026-10
- **Related**: [Database Migration](../database-migration.md)

## Context

The NVR needs to persist camera configuration, recording indexes, event records, transcode tasks, and more. The default database choice and multi-database compatibility strategy needed to be decided.

## Alternatives

**Option A: SQLite only**
- Zero ops overhead, single file, embedded
- Tradeoff: limited concurrent write performance (WAL mode helps), not suitable for multi-instance / high-concurrency scenarios

**Option B: PostgreSQL or MySQL as primary**
- Strong performance, high-concurrency writes, complex queries
- Tradeoff: requires operating a separate database service, raises deployment barrier

**Option C: SQLite as default, switchable to PostgreSQL / MySQL**
- Small deployments: zero dependencies, out-of-the-box
- Large-scale or high-availability deployments: switch to a production-grade database
- Tradeoff: all SQL must remain compatible across three dialects; avoid database-specific syntax

## Decision

**Option C**: SQLite as default, switchable via configuration to PostgreSQL or MySQL.

Implementation constraints:
- `internal/storage` uses `database/sql` standard library, no ORM
- All SQL compatible with three database dialects; avoid `RETURNING`, `UPSERT`, and similar dialect-specific features
- Migration versioning: DDL executed in order in `db.go`

## Tradeoffs and Known Limitations

- SQLite WAL mode offers good concurrent read performance, but high-frequency write scenarios (many cameras recording simultaneously) should switch to PostgreSQL
- Cross-driver migration (SQLite → PostgreSQL) requires export / import; live migration is not supported (see Database Migration docs)
- Cross-driver migration containing transcode data has not been fully validated as of 2026-10 and is not listed as a verified capability
