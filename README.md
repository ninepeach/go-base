# go-base

A lightweight Go infrastructure runtime for configuration, component registration, health aggregation and resource lifecycle management.

**Principle:** Manage resources, not business logic. Manage configuration, not its domain meaning. Manage lifecycle, not `main`.

## Status

Early implementation. The Core API types and registration scaffolding exist; runtime initialization, lifecycle orchestration, config decoding and providers are not yet complete. Not production-ready.

## Modules

- `core/` — runtime contracts and lifecycle (work in progress)
- `postgres/`, `redis/`, etc. — planned independent Go modules; not yet implemented

Go >= 1.25.1. Local development uses `go.work`; consuming applications should import individual modules.

## Architecture

The design source of truth is [Core v1 contract](https://github.com/ninepeach/architecture-docs/blob/main/projects/go-base/core.md).

## Development

```sh
go test ./core/...
go test -race ./core/...
```

Do not import infrastructure SDKs into the Core module.
