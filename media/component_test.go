package media

import (
	"context"
	"errors"
	"strings"
	"testing"

	base "github.com/ninepeach/go-base/core"
	postgres "github.com/ninepeach/go-base/postgres"
	s3 "github.com/ninepeach/go-base/s3"
)

func runtimeOptions(t *testing.T, f *fixture, cfg Config) base.Option {
	t.Helper()
	return base.Use(
		postgres.Register("primary", postgres.Config{DSN: testDSN(t)}),
		s3.Register("objects", s3.Config{Endpoint: strings.TrimPrefix(f.s3.srv.URL, "http://"), Region: "us-east-1", HealthBucket: testBucket}),
		Register("files", cfg),
	)
}

func TestComponentLifecycleThroughRuntime(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	cfg := f.cfg
	cfg.PostgresInstance, cfg.S3Instance = "primary", "objects"
	rt, err := base.New(ctx, runtimeOptions(t, f, cfg))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := FromComponent(rt, "files")
	if err != nil {
		t.Fatal(err)
	}
	file, err := svc.Upload(ctx, "a.txt", "text/plain", strings.NewReader("abc"), 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Get(ctx, file.ID); err != nil {
		t.Fatal(err)
	}
	if report := rt.Health(ctx); !report.Healthy {
		t.Fatalf("unhealthy: %+v", report)
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := FromComponent(rt, "files"); err == nil {
		t.Fatal("service must be unavailable after close")
	}
	// goleak in TestMain verifies the cleanup worker has exited.
}

func TestComponentStartFailsOnMissingBucketAndRollsBack(t *testing.T) {
	f := newFixture(t)
	cfg := f.cfg
	cfg.PostgresInstance, cfg.S3Instance, cfg.Bucket = "primary", "objects", "missing"
	_, err := base.New(context.Background(), runtimeOptions(t, f, cfg))
	if !errors.Is(err, s3.ErrBucketMissing) {
		t.Fatalf("want ErrBucketMissing, got %v", err)
	}
}

func TestComponentStartFailsOnMissingDependency(t *testing.T) {
	cfg := Config{PostgresInstance: "absent", S3Instance: "absent", Bucket: "files"}
	_, err := base.New(context.Background(), base.Use(Register("files", cfg)))
	if !errors.Is(err, base.ErrNotRegistered) {
		t.Fatalf("want ErrNotRegistered, got %v", err)
	}
}

func TestComponentHealthStates(t *testing.T) {
	c := &Component{}
	if err := c.Health(context.Background()); !errors.Is(err, base.ErrNotRunning) {
		t.Fatalf("before start: %v", err)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Health(context.Background()); !errors.Is(err, base.ErrClosed) {
		t.Fatalf("after close: %v", err)
	}
}
