module github.com/ninepeach/go-base/mailer

go 1.25.1

require (
	github.com/ninepeach/go-base/core v0.0.0
	go.uber.org/goleak v1.3.0
)

require (
	github.com/ninepeach/go-conf v0.0.0-20261117172750-54bef8051203 // indirect
	github.com/ninepeach/go-logger/v2 v2.0.1 // indirect
	golang.org/x/sys v0.38.0 // indirect
)

replace github.com/ninepeach/go-base/core => ../core
