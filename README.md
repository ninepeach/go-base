# go-base

A lightweight Go infrastructure runtime for configuration, component registration, health aggregation and resource lifecycle management.

**Principle:** Manage resources, not business logic. Manage configuration, not its domain meaning. Manage lifecycle, not `main`.

## Status

Pre-release. Core provides sequential startup with rollback, strict `Config.Decode` over go-conf `.conf` files, a go-logger/v2 backed `*slog.Logger`, health aggregation, lookup and single-flight shutdown. APIs may still change before v1.

## Modules

- `core/` — runtime contracts and lifecycle
- `postgres/` — pgxpool provider
- `redis/` — go-redis provider
- `mailer/` — SMTP STARTTLS provider
- `s3/` — S3-compatible (MinIO SDK) provider
- `media/` — file metadata + object storage capability module

## Configuration

Each provider instance reads `components.<provider>.<instance>` from the Runtime config file. Unknown keys and type mismatches fail startup; values are never echoed in errors.

```conf
app {
  name: 'example'
}
components {
  postgres {
    primary  { dsn: $DATABASE_URL }
    readonly { dsn: $READONLY_DATABASE_URL }
  }
  redis {
    default { address: 'localhost:6379', dial_timeout: '5s' }
  }
}
```

```go
rt, err := base.New(ctx,
    base.WithConfig("config/app.conf"),
    base.Use(
        postgres.Register("primary"),
        postgres.Register("readonly"),
        redis.Register("default"),
    ),
)
```

`$NAME` references a process environment variable (read as a literal string). Durations are Go duration strings. Every provider also offers `RegisterConfig(name, Config{...})` for explicit, code-supplied configuration that ignores the file.

Go >= 1.25.1. Local development uses `go.work`; consuming applications should import individual modules.

## Architecture

The design source of truth is [Core v1 contract](https://github.com/ninepeach/architecture-docs/blob/main/projects/go-base/core.md).

## Development

```sh
go test -race ./core/... ./postgres/... ./redis/... ./mailer/... ./s3/... ./media/...
```

Integration tests need `POSTGRES_TEST_DSN`, `MEDIA_TEST_DSN` and `REDIS_TEST_ADDR`; they are skipped otherwise.

Do not import infrastructure SDKs into the Core module.
