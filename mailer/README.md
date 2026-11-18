# go-base/mailer

A thin SMTP STARTTLS provider. Call `mailer.Register("default", cfg)` in `base.Use`, then `mailer.From(rt, "default").Send(ctx, recipients, rawRFC5322Message)`.

The caller owns message construction and MIME formatting. No background worker, retries, queue, or template engine. Requires STARTTLS; does not send credentials over plaintext. Connections are per-send and are not pooled.
