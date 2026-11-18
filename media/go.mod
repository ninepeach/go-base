module github.com/ninepeach/go-base/media

go 1.25.1

require (
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.7.6
	github.com/minio/minio-go/v7 v7.0.95
	github.com/ninepeach/go-base/core v0.0.0
	github.com/ninepeach/go-base/postgres v0.0.0
	github.com/ninepeach/go-base/s3 v0.0.0
)

replace github.com/ninepeach/go-base/core => ../core
replace github.com/ninepeach/go-base/postgres => ../postgres
replace github.com/ninepeach/go-base/s3 => ../s3
