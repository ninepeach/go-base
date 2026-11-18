# go-base/postgres

Thin pgxpool lifecycle adapter. `postgres.Register("primary", postgres.Config{DSN: dsn})` registers an owned pool. `postgres.From(rt, "primary")` returns `*pgxpool.Pool`. No SQL, migrations, or repository logic is added.
