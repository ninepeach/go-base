package redis

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	base "github.com/ninepeach/go-base/core"
)

func writeConf(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.conf")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRegisterRuntimeFromConfigFile(t *testing.T) {
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR not set")
	}
	path := writeConf(t, "components { redis { cache { address: '"+addr+"', db: 1, dial_timeout: '2s' } } }\n")
	ctx := context.Background()
	rt, err := base.New(ctx, base.WithConfig(path), base.Use(Register("cache")))
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close(ctx)
	client := From(rt, "cache")
	if client.Options().DB != 1 || client.Options().DialTimeout != 2*time.Second {
		t.Fatalf("file options not applied: db=%d dial=%s", client.Options().DB, client.Options().DialTimeout)
	}
}

func TestRegisterAppliesFileValuesBeforeConnecting(t *testing.T) {
	// Port 1 refuses quickly; the failure proves address and dial_timeout came from the file.
	path := writeConf(t, "components { redis { cache { address: '127.0.0.1:1', password: 'supersecret', dial_timeout: '200ms' } } }\n")
	_, err := base.New(context.Background(), base.WithConfig(path), base.Use(Register("cache")))
	if err == nil || !strings.Contains(err.Error(), "redis: ping") {
		t.Fatalf("expected ping failure, got %v", err)
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Fatalf("error leaks password: %v", err)
	}
}

func TestRegisterConfigFileErrors(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		body string
		want error
	}{
		"missing section": {"components { redis { other { address: 'x' } } }", base.ErrConfigNotFound},
		"unknown key":     {"components { redis { cache { addr: 'x' } } }", base.ErrConfigType},
		"db as string":    {"components { redis { cache { address: 'x', db: '1' } } }", base.ErrConfigType},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := base.New(ctx, base.WithConfig(writeConf(t, tc.body)), base.Use(Register("cache")))
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
	path := writeConf(t, "components { redis { cache { address: 'x', dial_timeout: '5 parsecs' } } }\n")
	_, err := base.New(ctx, base.WithConfig(path), base.Use(Register("cache")))
	if err == nil || !strings.Contains(err.Error(), "dial_timeout") || strings.Contains(err.Error(), "parsecs") {
		t.Fatalf("bad duration must name the field without echoing the value: %v", err)
	}
}
