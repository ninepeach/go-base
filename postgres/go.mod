module github.com/ninepeach/go-base/postgres

go 1.25.1

require (
	github.com/jackc/pgx/v5 v5.7.6
	github.com/ninepeach/go-base/core v0.0.0
	go.uber.org/goleak v1.3.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/ninepeach/go-conf v0.0.0-20261117172750-54bef8051203 // indirect
	github.com/ninepeach/go-logger/v2 v2.0.1 // indirect
	golang.org/x/crypto v0.37.0 // indirect
	golang.org/x/sync v0.13.0 // indirect
	golang.org/x/sys v0.38.0 // indirect
	golang.org/x/text v0.24.0 // indirect
)

replace github.com/ninepeach/go-base/core => ../core
