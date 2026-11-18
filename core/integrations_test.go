package base

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type configCapture struct {
	name  string
	value string
}

func (c *configCapture) Start(_ context.Context, env Env) error {
	c.name = env.Name()
	if env.Logger() == nil {
		return errors.New("missing component logger")
	}
	var config struct {
		DSN string `conf:"dsn"`
	}
	if err := env.Config().Decode("", &config); err != nil {
		return err
	}
	c.value = config.DSN
	return nil
}

func (*configCapture) Health(context.Context) error { return nil }
func (*configCapture) Close(context.Context) error  { return nil }

func TestRuntimeLoadsConfigAndComponentView(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.conf")
	data := "app { name: 'example' }\ncomponents { mock { primary { dsn: 'db-primary' } } }\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	c := &configCapture{}
	key := NewKey("mock")
	rt, err := New(context.Background(), WithConfig(path), Use(Register(key, "primary", func() (Component, error) {
		return c, nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close(context.Background())
	if c.name != "mock/primary" || c.value != "db-primary" {
		t.Fatalf("component configuration mismatch: %+v", c)
	}
	var app struct {
		Name string `conf:"name"`
	}
	if err := rt.Config().Decode("app", &app); err != nil {
		t.Fatal(err)
	}
	if app.Name != "example" || rt.Logger() == nil {
		t.Fatalf("invalid runtime configuration: %+v", app)
	}
}

func TestInvalidConfigFailsBeforeBuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.conf")
	if err := os.WriteFile(path, []byte("app { broken"), 0600); err != nil {
		t.Fatal(err)
	}
	built := false
	key := NewKey("mock")
	rt, err := New(context.Background(), WithConfig(path), Use(Register(key, "primary", func() (Component, error) {
		built = true
		return &mockComponent{}, nil
	})))
	if rt != nil || !errors.Is(err, ErrConfigType) || built {
		t.Fatalf("expected pre-build config failure, runtime=%v err=%v built=%v", rt, err, built)
	}
}
