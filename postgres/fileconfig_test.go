package postgres

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestRegisterReadsDSNFromConfigFile(t *testing.T) {
	// An unparsable DSN proves the file value reached Start without needing a server.
	t.Setenv("GO_BASE_PG_TEST_DSN", "postgres://user:supersecret@%zz/db")
	path := writeConf(t, "components { postgres { primary { dsn: $GO_BASE_PG_TEST_DSN } } }\n")
	_, err := base.New(context.Background(), base.WithConfig(path), base.Use(Register("primary")))
	if err == nil || !strings.Contains(err.Error(), "invalid connection configuration") {
		t.Fatalf("expected DSN parse failure, got %v", err)
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Fatalf("error leaks password: %v", err)
	}
}

func TestRegisterRuntimeFromConfigFile(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set")
	}
	t.Setenv("GO_BASE_PG_TEST_DSN", dsn)
	path := writeConf(t, "components { postgres { primary { dsn: $GO_BASE_PG_TEST_DSN } } }\n")
	ctx := context.Background()
	rt, err := base.New(ctx, base.WithConfig(path), base.Use(Register("primary")))
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close(ctx)
	if err := From(rt, "primary").Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterConfigFileErrors(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		body string
		want error
	}{
		"missing section": {"components { postgres { readonly { dsn: 'x' } } }", base.ErrConfigNotFound},
		"unknown key":     {"components { postgres { primary { DSN: 'x' } } }", base.ErrConfigType},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := base.New(ctx, base.WithConfig(writeConf(t, tc.body)), base.Use(Register("primary")))
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
	if _, err := base.New(ctx, base.Use(Register("primary"))); !errors.Is(err, base.ErrConfigNotFound) {
		t.Fatalf("no config file: want ErrConfigNotFound, got %v", err)
	}
}
