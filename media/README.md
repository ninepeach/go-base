# go-base/media

Media is a higher-level file resource service built on the independent PostgreSQL and S3 providers. It is **not** part of Core and does not wrap SQL queries, cache logic, or HTTP.

## Setup

1. Apply `schema.sql` to your PostgreSQL database.
2. Register and start `postgres.Register("primary", ...)` and `s3.Register("default", ...)` in Core.
3. Resolve the service with `media.From(rt, media.Config{PostgresInstance: "primary", S3Instance: "default", Bucket: "files"})`.
4. Call `Upload`, `Get`, `Open` or `Delete`.

The caller must close the stream returned by `Open`. Media does not own the PostgreSQL pool or S3 client; Runtime manages their lifecycle.

## Failure semantics

- Upload writes S3 first and then PostgreSQL. If the database insert fails, Media attempts S3 compensation using an independent 10-second context. A failed compensation can leave an orphan object; the returned error contains both failures.
- Delete marks metadata as `deleting` before removing S3 bytes, then removes the row. A failed deletion leaves a retryable row. Repeating `Delete(id)` resumes the operation.
- There is no distributed transaction across S3 and PostgreSQL. Concurrent deletion and upload require application-level coordination.
- Media v1 intentionally excludes transcoding, thumbnails, CDN, public URLs, access control, and background retries.
