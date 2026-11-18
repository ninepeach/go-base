# go-base/redis

Thin go-redis lifecycle adapter. `redis.Register("default")` registers an owned client configured by `components.redis.default` (keys: `address`, `username`, `password`, `db`, `dial_timeout` as a Go duration string such as `'5s'`); `redis.RegisterConfig("default", redis.Config{Address: addr})` uses an explicit config and ignores the file. `redis.From(rt, "default")` returns `*redis.Client`. No command wrappers, cache policies, or retries beyond SDK defaults.
