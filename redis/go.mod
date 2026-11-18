module github.com/ninepeach/go-base/redis

go 1.25.1

require (
	github.com/ninepeach/go-base/core v0.0.0
	github.com/redis/go-redis/v9 v9.17.0
)

replace github.com/ninepeach/go-base/core => ../core
