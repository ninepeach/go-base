module github.com/ninepeach/go-base/postgres

go 1.25.1

require (
	github.com/jackc/pgx/v5 v5.7.6
	github.com/ninepeach/go-base/core v0.0.0
)

replace github.com/ninepeach/go-base/core => ../core
