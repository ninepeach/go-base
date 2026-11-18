package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	base "github.com/ninepeach/go-base/core"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestStartRejectsInvalidConfigWithoutLeakingSecrets(t *testing.T) {
	ctx := context.Background()
	if err := (&Component{}).Start(ctx, nil); err == nil {
		t.Fatal("empty DSN must fail")
	}
	err := (&Component{cfg: Config{DSN: "postgres://user:supersecret@%zz/db"}}).Start(ctx, nil)
	if err == nil || strings.Contains(err.Error(), "supersecret") {
		t.Fatalf("invalid DSN error must not contain the password: %v", err)
	}
}

func TestStartFailsOnUnreachableServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := &Component{cfg: Config{DSN: "postgres://u:supersecret@127.0.0.1:1/db?connect_timeout=1"}}
	err := c.Start(ctx, nil)
	if err == nil {
		t.Fatal("unreachable server must fail")
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Fatalf("error leaks password: %v", err)
	}
	if c.Pool() != nil {
		t.Fatal("failed start must not keep a pool")
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
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set")
	}
	ctx := context.Background()
	rt, err := base.New(ctx, base.Use(RegisterConfig("primary", Config{DSN: dsn})))
	if err != nil {
		t.Fatal(err)
	}
	pool := From(rt, "primary")
	var one int
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("query: %v", err)
	}
	if _, ok, err := Lookup(rt, "other"); ok || err != nil {
		t.Fatalf("unknown instance: ok=%v err=%v", ok, err)
	}
	if report := rt.Health(ctx); !report.Healthy {
		t.Fatalf("unhealthy: %+v", report)
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Lookup(rt, "primary"); err == nil {
		t.Fatal("lookup after close must fail")
	}
}
