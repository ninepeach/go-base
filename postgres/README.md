# go-base/postgres

Thin pgxpool lifecycle adapter. `postgres.Register("primary")` registers an owned pool configured by `components.postgres.primary` in the Runtime config file (key: `dsn`); `postgres.RegisterConfig("primary", postgres.Config{DSN: dsn})` uses an explicit config and ignores the file. `postgres.From(rt, "primary")` returns `*pgxpool.Pool`. No SQL, migrations, or repository logic is added.
