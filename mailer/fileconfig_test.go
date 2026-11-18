package mailer

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

func TestRegisterReadsConfigFile(t *testing.T) {
	t.Setenv("GO_BASE_SMTP_TEST_PASSWORD", "supersecret")
	path := writeConf(t, `components {
  mailer {
    default {
      address: 'smtp.example.com:587'
      username: 'robot'
      password: $GO_BASE_SMTP_TEST_PASSWORD
      from: 'noreply@example.com'
      timeout: '45s'
    }
  }
}
`)
	ctx := context.Background()
	rt, err := base.New(ctx, base.WithConfig(path), base.Use(Register("default")))
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close(ctx)
	c := From(rt, "default")
	c.mu.RLock()
	cfg := c.cfg
	c.mu.RUnlock()
	if cfg.Address != "smtp.example.com:587" || cfg.Username != "robot" || cfg.Password != "supersecret" ||
		cfg.From != "noreply@example.com" || cfg.Timeout != 45*time.Second || cfg.ServerName != "smtp.example.com" {
		t.Fatalf("unexpected decoded config: address=%q from=%q timeout=%s", cfg.Address, cfg.From, cfg.Timeout)
	}
}

func TestRegisterConfigFileErrors(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		body string
		want error
	}{
		"missing section": {"components { mailer { other { address: 'x:25' } } }", base.ErrConfigNotFound},
		"unknown key":     {"components { mailer { default { address: 'x:25', from: 'a@b', tls: true } } }", base.ErrConfigType},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := base.New(ctx, base.WithConfig(writeConf(t, tc.body)), base.Use(Register("default")))
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
	path := writeConf(t, "components { mailer { default { address: 'x:25', from: 'a@b', timeout: 'soon' } } }\n")
	_, err := base.New(ctx, base.WithConfig(path), base.Use(Register("default")))
	if err == nil || !strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "soon") {
		t.Fatalf("bad duration must name the field without echoing the value: %v", err)
	}
}
