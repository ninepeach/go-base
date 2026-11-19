module github.com/ninepeach/go-base/media

go 1.25.1

require (
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.7.6
	github.com/minio/minio-go/v7 v7.0.95
	github.com/ninepeach/go-base/core v0.0.0
	github.com/ninepeach/go-base/postgres v0.0.0
	github.com/ninepeach/go-base/s3 v0.0.0
	go.uber.org/goleak v1.3.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-ini/ini v1.67.0 // indirect
	github.com/goccy/go-json v0.10.5 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.18.0 // indirect
	github.com/klauspost/cpuid/v2 v2.2.11 // indirect
	github.com/minio/crc64nvme v1.0.2 // indirect
	github.com/minio/md5-simd v1.1.2 // indirect
	github.com/ninepeach/go-conf v0.0.0-20261117172750-54bef8051203 // indirect
	github.com/ninepeach/go-logger/v2 v2.0.3 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/tinylib/msgp v1.3.0 // indirect
	golang.org/x/crypto v0.39.0 // indirect
	golang.org/x/net v0.41.0 // indirect
	golang.org/x/sync v0.15.0 // indirect
	golang.org/x/sys v0.38.0 // indirect
	golang.org/x/text v0.26.0 // indirect
)

replace github.com/ninepeach/go-base/core => ../core

replace github.com/ninepeach/go-base/postgres => ../postgres

replace github.com/ninepeach/go-base/s3 => ../s3
