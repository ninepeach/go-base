package s3

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	base "github.com/ninepeach/go-base/core"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
	)
}

// bucketServer answers HEAD /<bucket> with 200 for "present" and 404 otherwise.
func bucketServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Trim(r.URL.Path, "/") == "present" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestStartRejectsInvalidConfig(t *testing.T) {
	ctx := context.Background()
	for _, cfg := range []Config{{}, {Endpoint: "localhost:9000", AccessKey: "only-access"}} {
		if err := (&Component{cfg: cfg}).Start(ctx, nil); err == nil {
			t.Fatalf("accepted invalid config %+v", cfg)
		}
	}
}

func TestHealthBucketChecks(t *testing.T) {
	endpoint := bucketServer(t)
	ctx := context.Background()
	missing := &Component{cfg: Config{Endpoint: endpoint, Region: "us-east-1", HealthBucket: "absent"}}
	if err := missing.Start(ctx, nil); !errors.Is(err, ErrBucketMissing) {
		t.Fatalf("want ErrBucketMissing, got %v", err)
	}
	if missing.Client() != nil {
		t.Fatal("failed start must not keep a client")
	}
	ok := &Component{cfg: Config{Endpoint: endpoint, Region: "us-east-1", HealthBucket: "present"}}
	if err := ok.Start(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := ok.Health(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeLifecycle(t *testing.T) {
	endpoint := bucketServer(t)
	ctx := context.Background()
	rt, err := base.New(ctx, base.Use(Register("objects", Config{Endpoint: endpoint, Region: "us-east-1", HealthBucket: "present"})))
	if err != nil {
		t.Fatal(err)
	}
	if From(rt, "objects") == nil {
		t.Fatal("nil client")
	}
	if report := rt.Health(ctx); !report.Healthy {
		t.Fatalf("unhealthy: %+v", report)
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Lookup(rt, "objects"); err == nil {
		t.Fatal("lookup after close must fail")
	}
}

func TestLifecycleStates(t *testing.T) {
	c := &Component{}
	if err := c.Health(context.Background()); !errors.Is(err, base.ErrNotRunning) {
		t.Fatalf("before start: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := c.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Health(context.Background()); !errors.Is(err, base.ErrClosed) {
		t.Fatalf("after close: %v", err)
	}
}
