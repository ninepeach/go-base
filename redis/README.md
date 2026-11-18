# go-base/redis

Thin go-redis lifecycle adapter. `redis.Register("default", redis.Config{Address: addr})` registers an owned client. `redis.From(rt, "default")` returns `*redis.Client`. No command wrappers, cache policies, or retries beyond SDK defaults.
