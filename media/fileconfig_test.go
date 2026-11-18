package media

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

// captureEnv runs loadConfig against the Env the Runtime builds for media/files.
type captureEnv struct {
	cfg Config
	err error
}

func (c *captureEnv) Start(_ context.Context, env base.Env) error {
	c.cfg, c.err = loadConfig(env)
	return nil
}
func (c *captureEnv) Health(context.Context) error { return nil }
func (c *captureEnv) Close(context.Context) error  { return nil }

func decodeFile(t *testing.T, body string) (Config, error) {
	t.Helper()
	probe := &captureEnv{}
	ctx := context.Background()
	rt, err := base.New(ctx, base.WithConfig(writeConf(t, body)), base.Use(
		base.Register(key, "files", func() (base.Component, error) { return probe, nil }),
	))
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatal(err)
	}
	return probe.cfg, probe.err
}

func TestLoadConfigFromFile(t *testing.T) {
	cfg, err := decodeFile(t, `components {
  media {
    files {
      postgres_instance: 'primary'
      s3_instance: 'objects'
      bucket: 'files'
      prefix: 'uploads'
      max_size: 100mb
      max_upload_duration: '10m'
      cleanup_interval: '1m'
      stale_after: '15m'
      schema: 'media'
      table: 'objects'
    }
  }
}
`)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		PostgresInstance: "primary", S3Instance: "objects", Bucket: "files", Prefix: "uploads",
		MaxSize: 100 << 20, MaxUploadDuration: 10 * time.Minute, CleanupInterval: time.Minute,
		StaleAfter: 15 * time.Minute, Schema: "media", Table: "objects",
	}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadConfigFileErrors(t *testing.T) {
	if _, err := decodeFile(t, "components { media { files { bucket: 'b', ttl: '1m' } } }"); !errors.Is(err, base.ErrConfigType) {
		t.Fatalf("unknown key: %v", err)
	}
	if _, err := decodeFile(t, "components { media { other { bucket: 'b' } } }"); !errors.Is(err, base.ErrConfigNotFound) {
		t.Fatalf("missing section: %v", err)
	}
	_, err := decodeFile(t, "components { media { files { bucket: 'b', stale_after: 'later' } } }")
	if err == nil || !strings.Contains(err.Error(), "stale_after") || strings.Contains(err.Error(), "later") {
		t.Fatalf("bad duration must name the field without echoing the value: %v", err)
	}
}

func TestRegisterFromFileFailsStartWithoutDependencies(t *testing.T) {
	path := writeConf(t, "components { media { files { postgres_instance: 'primary', s3_instance: 'objects', bucket: 'files' } } }\n")
	_, err := base.New(context.Background(), base.WithConfig(path), base.Use(Register("files")))
	if !errors.Is(err, base.ErrNotRegistered) || !strings.Contains(err.Error(), `"primary"`) {
		t.Fatalf("expected missing postgres dependency from file config, got %v", err)
	}
}
