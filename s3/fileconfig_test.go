package s3

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

func TestRegisterReadsConfigFile(t *testing.T) {
	endpoint := bucketServer(t)
	t.Setenv("GO_BASE_S3_TEST_SECRET", "supersecret")
	path := writeConf(t, `components {
  s3 {
    objects {
      endpoint: '`+endpoint+`'
      access_key: 'AKIDEXAMPLE'
      secret_key: $GO_BASE_S3_TEST_SECRET
      use_tls: false
      region: 'us-east-1'
      health_bucket: 'present'
    }
  }
}
`)
	ctx := context.Background()
	rt, err := base.New(ctx, base.WithConfig(path), base.Use(Register("objects")))
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close(ctx)
	if From(rt, "objects") == nil {
		t.Fatal("expected client")
	}
	c, err := rt.Get(key, "objects")
	if err != nil {
		t.Fatal(err)
	}
	got := c.(*Component).cfg
	if got.Endpoint != endpoint || got.AccessKey != "AKIDEXAMPLE" || got.SecretKey != "supersecret" || got.UseTLS || got.HealthBucket != "present" {
		t.Fatalf("unexpected decoded config: endpoint=%q bucket=%q", got.Endpoint, got.HealthBucket)
	}
}

func TestRegisterConfigFileErrors(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		body string
		want error
	}{
		"missing section": {`components { s3 { other { endpoint: 'x' } } }`, base.ErrConfigNotFound},
		"no config file":  {"", base.ErrConfigNotFound},
		"unknown key":     {`components { s3 { objects { endpoint: 'x', secret: 'supersecret' } } }`, base.ErrConfigType},
		"wrong type":      {`components { s3 { objects { endpoint: 'x', use_tls: 'yes' } } }`, base.ErrConfigType},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			opts := []base.Option{base.Use(Register("objects"))}
			if tc.body != "" {
				opts = append(opts, base.WithConfig(writeConf(t, tc.body)))
			}
			_, err := base.New(ctx, opts...)
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			if strings.Contains(err.Error(), "supersecret") {
				t.Fatalf("error leaks config value: %v", err)
			}
		})
	}
}
