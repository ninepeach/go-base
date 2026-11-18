# go-base/s3

Thin S3-compatible lifecycle adapter using MinIO Go SDK. `s3.Register("default", s3.Config{Endpoint: endpoint, AccessKey: key, SecretKey: secret, UseTLS: true})` registers a client. `s3.From(rt, "default")` returns `*minio.Client`, exposing the SDK's PutObject/GetObject/RemoveObject operations. `HealthBucket` optionally enables a live BucketExists check; without it Health checks only local initialization and context state. Close invalidates the provider's handle but cannot revoke SDK handles previously handed to callers.
