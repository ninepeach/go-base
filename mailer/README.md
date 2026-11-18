# go-base/mailer

A thin SMTP STARTTLS provider. Call `mailer.Register("default")` in `base.Use` to read `components.mailer.default` (keys: `address`, `username`, `password`, `from`, `server_name`, `timeout` as a Go duration string), or `mailer.RegisterConfig("default", cfg)` for an explicit config. `TLSConfig` (private CAs) is only available through `RegisterConfig`. Then call then `mailer.From(rt, "default").Send(ctx, recipients, rawRFC5322Message)`.

The caller owns message construction and MIME formatting. No background worker, retries, queue, or template engine. Requires STARTTLS; does not send credentials over plaintext. Connections are per-send and are not pooled.
