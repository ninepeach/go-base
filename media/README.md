# go-base/media

Media is a **capability module**, not a thin infrastructure provider. PostgreSQL, Redis, S3 and Mailer remain thin lifecycle providers without business tables. Media owns only its internal file metadata; the application owns file associations and all authorization decisions.

## Lifecycle

Register providers before Media, in this exact order. Each instance reads its own `components.<provider>.<instance>` section:

```conf
components {
  postgres { primary { dsn: $DATABASE_URL } }
  s3 {
    objects {
      endpoint: 's3.example.com'
      access_key: $S3_ACCESS_KEY
      secret_key: $S3_SECRET_KEY
      use_tls: true
      health_bucket: 'files'
    }
  }
  media {
    files {
      postgres_instance: 'primary'
      s3_instance: 'objects'
      bucket: 'files'
      max_size: 100mb
      max_upload_duration: '10m'
      cleanup_interval: '1m'
      stale_after: '15m'
    }
  }
}
```

```go
rt, err := base.New(ctx,
    base.WithConfig("config/app.conf"),
    base.Use(
        postgres.Register("primary"),
        s3.Register("objects"),
        media.Register("files"),
    ),
)
if err != nil { return err }
files, err := media.FromComponent(rt, "files")
// ... on shutdown, after stopping request intake:
shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
defer cancel()
err = rt.Close(shutdownCtx)
```

Media config keys: `postgres_instance`, `s3_instance`, `bucket`, `prefix`, `max_size`, `schema`, `table`, and Go duration strings `max_upload_duration`, `cleanup_interval`, `stale_after`. `media.RegisterConfig("files", media.Config{...})` takes an explicit config instead of the file.

The runtime's `Env.Lookup` only exposes previously started components. Media starts its own reconciliation goroutine, which Runtime closes in reverse registration order before PostgreSQL and S3. Do not close provider clients independently while Media is running.

## Migrations

Media never migrates on its own. Run `media.Migrate(ctx, db, cfg)` as a deployment step, before starting new instances. `Component.Start` checks the schema version and fails with `ErrSchemaOutdated` if the database is older than the code.

- **Versioned, forward-only.** Applied versions are recorded in `<table>_schema_migrations` in the same schema. `media.SchemaVersion()` is the version the code requires; `media.CurrentVersion` reports the database's.
- **Safe to repeat and to run concurrently.** A transaction-scoped advisory lock serializes concurrent runners, and pending steps run in one transaction together with their version records. A failed migration leaves the schema unchanged, so rerunning after the fix starts from the same version.
- **Rolling deployments.** Old code keeps starting on a newer schema (steps must stay backward compatible), but `Migrate` from old code refuses a newer schema (`ErrSchemaTooNew`) and never downgrades.
- **Existing databases.** A database created before versioning (via `MigrationSQL` or `schema.sql`) is adopted by the first `Migrate` run.
- **External runners.** `media.Migrations(cfg)` returns the rendered steps; if you use your own runner, you own version tracking and must still create the versions table for the startup check.

Steps are append-only: never edit or reorder an existing one. Use only transactional DDL (no `CREATE INDEX CONCURRENTLY`).

## File protocol

Every state transition is a compare-and-set on `state`; `ready` is only ever reached from `pending`.

- **Upload** inserts a `pending` row with a UUID-derived object key, then streams exactly the declared size to S3. Input longer or shorter than declared fails with `ErrSizeMismatch` (input must reach EOF). Only after the object is stored and verified does `pending → ready` happen. Pending files are invisible to `Get`, `Open`, `PresignedGet` and cannot be deleted.
- **Timing invariant:** an upload is cancelled after `MaxUploadDuration`, and `StaleAfter` must be at least `MaxUploadDuration` + 1 minute. Cleanup therefore never reclaims a pending row while its upload can still write. No heartbeat is needed.
- **Upload failure** tombstones the row (`deleting`) and removes the object immediately, but leaves the row for cleanup. Cleanup removes the object a second time after `StaleAfter`, so an S3 write that commits late after a client-side cancellation cannot survive without metadata.
- If cleanup claims a pending row before finalization, `Upload` returns `ErrUploadAbandoned`; the row never becomes ready.
- **Delete** moves `ready → deleting`, removes the object, then the row. If S3 removal fails the file is already unreadable (`ErrDeleting`) and cleanup retries.
- **Cleanup** is safe across many instances: one statement claims up to 100 expired `pending`/`deleting` rows with `FOR UPDATE SKIP LOCKED`, and claiming refreshes `updated_at`, which acts as a lease of length `StaleAfter`. A crashed instance's claims expire and are retried. A worker that resumes after its lease expired is harmless without fencing: `deleting` is terminal, object keys are never reused, and object and row removal are idempotent.
- `PresignedGet` returns a short-lived S3 URL (max 7 days); the **application must authorize** the media ID first. `Open` returns a reader the caller must close.
- Filename and content type must be 1–255 bytes of valid UTF-8 without control characters. `MaxSize` defaults to 100 MiB.

Configure an `AbortIncompleteMultipartUpload` lifecycle rule on the bucket; uploads above the client's part size use multipart, and parts of cancelled uploads are not visible objects.

No image processing, CDN management, public access policies, or business ownership logic is included.

## Testing

Integration tests run against a real PostgreSQL (`MEDIA_TEST_DSN`) and an in-process S3 fake with fault injection; they cover size mismatch, storage failures, timeouts, pending visibility, finalize-vs-cleanup races, cleanup leases and concurrent cleanup from 8 instances. Without `MEDIA_TEST_DSN` they are skipped. CI provides PostgreSQL as a service.
