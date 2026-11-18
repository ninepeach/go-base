# go-base/media

Media is a **capability module**, not a thin infrastructure provider. PostgreSQL, Redis, S3 and Mailer remain thin lifecycle providers without business tables. Media owns only its internal file metadata; the application owns file associations and all authorization decisions.

## Lifecycle

Register providers before Media, in this exact order:

```go
rt, err := base.New(ctx, base.Use(
    postgres.Register("primary", postgres.Config{DSN: dsn}),
    s3.Register("objects", s3.Config{Endpoint: endpoint, AccessKey: accessKey, SecretKey: secretKey, UseTLS: true}),
    media.Register("files", media.Config{
        PostgresInstance: "primary",
        S3Instance: "objects",
        Bucket: "files",
        MaxSize: 100 << 20,
        MaxUploadDuration: 10 * time.Minute,
        CleanupInterval: time.Minute,
        StaleAfter: 15 * time.Minute,
    }),
))
if err != nil { return err }
defer rt.Close(context.Background())
files, err := media.FromComponent(rt, "files")
```

The runtime's `Env.Lookup` only exposes previously started components. Media starts its own reconciliation goroutine, which Runtime closes in reverse registration order before PostgreSQL and S3. Do not close provider clients independently while Media is running.

## Migrations

Media does **not** auto-migrate. Apply `media.MigrationSQL(cfg)` with your migration runner, or explicitly call `media.Migrate(ctx, db, cfg)` before Runtime startup. Set `Schema` and `Table` on Config to choose SQL identifiers; identifiers are strictly validated. `schema.sql` is the default-schema reference migration, not a substitute for migration versioning.

## File protocol

- Upload inserts a `pending` row containing a UUID-derived object key before uploading to S3. On successful upload it transitions to `ready`. Failed uploads leave a trackable pending record.
- Delete transitions the record to `deleting`, removes the S3 object, and deletes the record. Failed operations are retryable.
- The cleanup worker periodically removes expired `pending` objects and resumes expired `deleting` operations. Its staleness threshold must exceed the configured `MaxUploadDuration` (default 10 minutes; `StaleAfter` defaults to 15 minutes). Pending uploads refresh their lease periodically. If PostgreSQL is unavailable during a long upload, lease refresh can fail, so cleanup remains best effort.
- `PresignedGet` returns a short-lived S3 URL; the **application must authorize** the media ID before calling it.
- `Open` streams an object and returns a reader that the caller must close.
- Uploads require a known content length and enforce `MaxSize` (default 100 MiB) and `MaxUploadDuration`. No image processing, CDN management, public access policies, or business ownership logic is included.

**Important:** PostgreSQL and S3 cannot participate in one atomic transaction. Cleanup is best effort; S3 operations can succeed even when the client sees a network error. Concurrent upload, delete and cleanup, multiple worker instances, crash recovery, and migration upgrades require integration and fault-injection tests before production use.
