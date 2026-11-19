# Project Instructions — go-base

Follow the shared global AI engineering rules. This file adds repository-specific context only.

## Purpose
Lightweight Go infrastructure runtime for configuration, component registration, health aggregation, and resource lifecycle.

## Boundaries
- Manage resources and lifecycle, not business logic.
- Keep infrastructure SDKs out of the `core` module.
- Preserve strict configuration validation, startup rollback, health aggregation, and shutdown contracts.
- This repository uses separate modules and `go.work`; consumers import individual modules.
- Read the Core v1 architecture contract linked from README before changing core semantics.

## Verification
- Run `go test -race ./core/... ./postgres/... ./redis/... ./mailer/... ./s3/... ./media/...` from the workspace.
- Integration tests require external test DSNs/addresses; report skipped tests accurately.
