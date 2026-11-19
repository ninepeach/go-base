package base

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.conf")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigFileErrorsAreDistinguishable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.conf")
	_, err := New(context.Background(), WithConfig(missing))
	if !errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrConfigParse) {
		t.Fatalf("missing file: %v", err)
	}
	if !strings.Contains(err.Error(), "absent.conf") {
		t.Fatalf("missing file error should name the path: %v", err)
	}
}

func TestConfigParseErrorsNeverEchoValues(t *testing.T) {
	t.Setenv("GO_BASE_SET_VAR", "x")
	cases := map[string]struct {
		body string
		want string
	}{
		"syntax":      {"a {\n  secret: 'supersecret'\n  ]]] 'supersecret2'\n", "line 3"},
		"missing env": {"a {\n  dsn: $GO_BASE_UNSET_VAR_FOR_TEST\n}\n", "GO_BASE_UNSET_VAR_FOR_TEST referenced on line 2"},
		"duplicate":   {"a: 'supersecret'\na: 'supersecret'\n", "duplicate key"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := New(context.Background(), WithConfig(writeConfigFile(t, tc.body)))
			if !errors.Is(err, ErrConfigParse) {
				t.Fatalf("want ErrConfigParse, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q should contain %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "supersecret") {
				t.Fatalf("error leaks a config value: %v", err)
			}
		})
	}
}

type loggingComponent struct{ mockComponent }

func (c *loggingComponent) Start(_ context.Context, env Env) error {
	env.Logger().Info("started")
	return nil
}

func TestWithLogger(t *testing.T) {
	if _, err := New(context.Background(), WithLogger(nil)); err == nil {
		t.Fatal("nil logger accepted")
	}
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil))
	rt, err := New(context.Background(), WithLogger(l), Use(Register(NewKey("mock"), "primary", func() (Component, error) {
		return &loggingComponent{}, nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	if rt.Logger() != l {
		t.Fatal("runtime must use the supplied logger")
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "component=mock/primary") || !strings.Contains(buf.String(), "started") {
		t.Fatalf("component logger must derive from the supplied logger: %q", buf.String())
	}
}
