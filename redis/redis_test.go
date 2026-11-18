package redis

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	base "github.com/ninepeach/go-base/core"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestStartRejectsInvalidConfig(t *testing.T) {
	ctx := context.Background()
	for _, cfg := range []Config{{}, {Address: "127.0.0.1:6379", DB: -1}, {Address: "127.0.0.1:6379", DialTimeout: -1}} {
		if err := (&Component{cfg: cfg}).Start(ctx, nil); err == nil {
			t.Fatalf("accepted invalid config %+v", cfg)
		}
	}
}

func TestStartFailsOnUnreachableServerAndReleasesClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := &Component{cfg: Config{Address: "127.0.0.1:1", DialTimeout: 500 * time.Millisecond}}
	if err := c.Start(ctx, nil); err == nil {
		t.Fatal("unreachable server must fail")
	}
	if c.Client() != nil {
		t.Fatal("failed start must not keep a client")
	}
}

func TestLifecycleStatesWithoutServer(t *testing.T) {
	c := &Component{}
	if err := c.Health(context.Background()); !errors.Is(err, base.ErrNotRunning) {
		t.Fatalf("before start: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := c.Close(context.Background()); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}
	if err := c.Health(context.Background()); !errors.Is(err, base.ErrClosed) {
		t.Fatalf("after close: %v", err)
	}
}

func TestRuntimeIntegration(t *testing.T) {
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR not set")
	}
	ctx := context.Background()
	rt, err := base.New(ctx, base.Use(Register("cache", Config{Address: addr})))
	if err != nil {
		t.Fatal(err)
	}
	client := From(rt, "cache")
	if err := client.Set(ctx, "go-base:test", "v", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if report := rt.Health(ctx); !report.Healthy {
		t.Fatalf("unhealthy: %+v", report)
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.Ping(ctx).Err(); err == nil {
		t.Fatal("client must be closed after Runtime.Close")
	}
}
