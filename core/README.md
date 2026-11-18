# go-base/core

Core v1 implementation workspace.

Architecture specification: https://github.com/ninepeach/architecture-docs/blob/main/projects/go-base/core.md

Status: Core lifecycle and strict configuration decoding are implemented. Provider SDK modules remain separate.

Go >= 1.25.1. This module must not depend on PostgreSQL, Redis, SMTP or S3 SDKs.

## Startup rollback

`New` starts registered components in registration order. If a Build or Start
operation fails (or the caller context is canceled before the next component),
Core closes **previously started** components in reverse registration order.

Rollback uses a new `context.Background()` with a **10-second timeout**, not
the caller's context. This allows cleanup to proceed even if the caller's
context has already been canceled. Close errors are joined with the startup
failure. A component whose own `Start` fails is responsible for cleaning up
resources it partially initialized; Core rolls back only components whose
`Start` completed successfully.

The timeout is a cooperative deadline: components must honor the context for
bounded cleanup; Core cannot forcibly interrupt a blocking `Close`.

## Configuration and logging

Use `WithConfig("config/app.conf")` to load a go-conf configuration file.
Parsing is strict: duplicate keys are rejected, includes are confined to the
configuration file directory, and environment references remain strings.
Configuration parsing finishes before any component Build/Start is invoked.
The runtime retains a read-only snapshot; `rt.Config()` decodes from its root,
while `Env.Config()` decodes relative to
`components.<provider>.<instance>`. Decode never exposes the source map.

Example:

```conf
app {
  name: 'example'
}
components {
  mock {
    primary {
      dsn: 'db-primary'
    }
  }
}
```

Core uses go-logger/v2's slog adapter and exposes only `*slog.Logger`.
`rt.Logger()` returns the runtime logger; each component's
`Env.Logger()` is bound to its full component name. The default logger
writes to stderr. Core does not own a file or syslog writer and does not
close any caller-provided writer.

The runtime does not automatically convert environment strings to numbers.
Explicit typed environment override mapping is not yet exposed by Core.
