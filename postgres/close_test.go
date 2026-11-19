package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	base "github.com/ninepeach/go-base/core"
)

func TestCloseHonorsContextWhileConnectionsAreAcquired(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set")
	}
	rt, err := base.New(context.Background(), base.Use(RegisterConfig("primary", Config{DSN: dsn})))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := From(rt, "primary").Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = rt.Close(ctx)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Close ignored its context: took %v", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline error, got %v", err)
	}
	conn.Release()
	// The first, timed-out Close still recorded its result; later calls return it.
	if err := rt.Close(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("repeated Runtime.Close must return the first result: %v", err)
	}
}

func TestRepeatedCloseWaitsForPoolShutdown(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set")
	}
	c := &Component{cfg: Config{DSN: dsn}}
	if err := c.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	conn, err := c.Pool().Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := c.Close(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Close: %v", err)
	}
	if c.Pool() != nil {
		t.Fatal("pool must be unavailable once Close started")
	}
	// A second Close must not report success while the pool is still closing.
	again, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	if err := c.Close(again); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Close reported early success: %v", err)
	}
	go func() { time.Sleep(50 * time.Millisecond); conn.Release() }()
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close after release: %v", err)
	}
	if err := c.Close(nil); err == nil { //nolint:staticcheck // nil context is the case under test
		t.Fatal("nil context accepted")
	}
}
