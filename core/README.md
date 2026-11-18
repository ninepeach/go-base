# go-base/core

Core v1 implementation workspace.

Architecture specification: https://github.com/ninepeach/architecture-docs/blob/main/projects/go-base/core.md

Status: initial scaffolding. Runtime lifecycle, configuration decoding and provider modules are not yet implemented. Do not treat this package as production-ready.

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
