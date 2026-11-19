package media

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	base "github.com/ninepeach/go-base/core"
	postgres "github.com/ninepeach/go-base/postgres"
	s3 "github.com/ninepeach/go-base/s3"
)

var key = base.NewKey("media")

// Component resolves dependencies during Start and owns the reconciliation worker.
// It does not own the underlying PostgreSQL or S3 connections.
type Component struct {
	cfg      Config
	fromFile bool
	mu       sync.RWMutex
	service  *Service
	cancel   context.CancelFunc
	done     chan struct{}
	closed   bool
}

// fileConfig is the shape of components.media.<instance> in the config file.
// Durations are Go duration strings such as "10m"; max_size accepts go-conf
// size suffixes such as 100mb.
type fileConfig struct {
	PostgresInstance  string `conf:"postgres_instance"`
	S3Instance        string `conf:"s3_instance"`
	Bucket            string `conf:"bucket"`
	Prefix            string `conf:"prefix"`
	MaxSize           int64  `conf:"max_size"`
	MaxUploadDuration string `conf:"max_upload_duration"`
	Schema            string `conf:"schema"`
	Table             string `conf:"table"`
	CleanupInterval   string `conf:"cleanup_interval"`
	StaleAfter        string `conf:"stale_after"`
}

// Register registers an instance configured by the components.media.<name>
// section of the Runtime config file. The section is decoded during Start;
// a missing section or unknown key fails startup.
func Register(name string, opts ...base.RegistrationOption) base.Registration {
	return base.Register(key, name, func() (base.Component, error) {
		return &Component{fromFile: true}, nil
	}, opts...)
}

// RegisterConfig registers an instance with an explicit Config. The config
// file is not consulted for this instance.
func RegisterConfig(name string, cfg Config, opts ...base.RegistrationOption) base.Registration {
	return base.Register(key, name, func() (base.Component, error) {
		return &Component{cfg: cfg}, nil
	}, opts...)
}

func loadConfig(env base.Env) (Config, error) {
	if env == nil {
		return Config{}, errors.New("media: config: nil env")
	}
	var fc fileConfig
	if err := env.Config().Decode("", &fc); err != nil {
		return Config{}, fmt.Errorf("media: config: %w", err)
	}
	cfg := Config{
		PostgresInstance: fc.PostgresInstance,
		S3Instance:       fc.S3Instance,
		Bucket:           fc.Bucket,
		Prefix:           fc.Prefix,
		MaxSize:          fc.MaxSize,
		Schema:           fc.Schema,
		Table:            fc.Table,
	}
	for _, d := range []struct {
		field string
		src   string
		dst   *time.Duration
	}{
		{"max_upload_duration", fc.MaxUploadDuration, &cfg.MaxUploadDuration},
		{"cleanup_interval", fc.CleanupInterval, &cfg.CleanupInterval},
		{"stale_after", fc.StaleAfter, &cfg.StaleAfter},
	} {
		if d.src == "" {
			continue
		}
		v, err := time.ParseDuration(d.src)
		if err != nil {
			return Config{}, fmt.Errorf("media: config: %s is not a valid duration", d.field)
		}
		*d.dst = v
	}
	return cfg, nil
}

func (c *Component) Start(ctx context.Context, env base.Env) error {
	if c.fromFile {
		cfg, err := loadConfig(env)
		if err != nil {
			return err
		}
		c.cfg = cfg
	}
	db, ok, err := postgres.LookupEnv(env, c.cfg.PostgresInstance)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("media: missing postgres dependency %q: %w", c.cfg.PostgresInstance, base.ErrNotRegistered)
	}
	store, ok, err := s3.LookupEnv(env, c.cfg.S3Instance)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("media: missing s3 dependency %q: %w", c.cfg.S3Instance, base.ErrNotRegistered)
	}
	service, err := New(db, store, c.cfg)
	if err != nil {
		return err
	}
	if err := db.Ping(ctx); err != nil {
		return fmt.Errorf("media: postgres health: %w", err)
	}
	version, err := checkSchema(ctx, db, service.cfg)
	if err != nil {
		return err
	}
	if version > SchemaVersion() {
		env.Logger().Warn("media schema is newer than this code", "database", version, "code", SchemaVersion())
	}
	exists, err := store.BucketExists(ctx, service.cfg.Bucket)
	if err != nil {
		return fmt.Errorf("media: bucket check: %w", err)
	}
	if !exists {
		return fmt.Errorf("media: %w: %s", s3.ErrBucketMissing, service.cfg.Bucket)
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		cancel()
		return base.ErrClosed
	}
	c.service, c.cancel, c.done = service, cancel, done
	c.mu.Unlock()
	go runCleanup(workerCtx, done, service, env.Logger())
	return nil
}

func runCleanup(ctx context.Context, done chan<- struct{}, service *Service, log *slog.Logger) {
	defer close(done)
	ticker := time.NewTicker(service.cfg.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pass, stop := context.WithTimeout(ctx, cleanupBatchTimeout)
			if err := service.Cleanup(pass); err != nil && ctx.Err() == nil {
				log.Warn("media cleanup failed", "error", err)
			}
			stop()
		}
	}
}

// Service returns nil before Start and after Close.
func (c *Component) Service() *Service {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.service
}

func (c *Component) Health(ctx context.Context) error {
	c.mu.RLock()
	service, closed := c.service, c.closed
	c.mu.RUnlock()
	if closed {
		return base.ErrClosed
	}
	if service == nil {
		return base.ErrNotRunning
	}
	return service.db.Ping(ctx)
}

// Close stops the cleanup worker and waits for it to exit. Concurrent or
// repeated calls share the same completion channel. A caller whose context
// expires may return early, but later callers can still wait for worker exit.
// The owning runtime must not close dependencies until this worker has exited.
func (c *Component) Close(ctx context.Context) error {
	if ctx == nil {
		return errors.New("media: nil close context")
	}
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.service, c.cancel, c.closed = nil, nil, true
	// Retain c.done: a second Close must not report success while the
	// first caller has timed out and cleanup is still running.
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func Lookup(rt *base.Runtime, name string) (*Service, bool, error) {
	component, err := rt.Get(key, name)
	if errors.Is(err, base.ErrNotRegistered) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	c, ok := component.(*Component)
	if !ok {
		return nil, false, errors.New("media: unexpected component type")
	}
	service := c.Service()
	if service == nil {
		return nil, false, base.ErrNotRunning
	}
	return service, true, nil
}

func FromComponent(rt *base.Runtime, name string) (*Service, error) {
	service, ok, err := Lookup(rt, name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, base.ErrNotRegistered
	}
	return service, nil
}
