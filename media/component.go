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
	cfg     Config
	mu      sync.RWMutex
	service *Service
	cancel  context.CancelFunc
	done    chan struct{}
	closed  bool
}

func Register(name string, cfg Config, opts ...base.RegistrationOption) base.Registration {
	return base.Register(key, name, func() (base.Component, error) {
		return &Component{cfg: cfg}, nil
	}, opts...)
}

func (c *Component) Start(ctx context.Context, env base.Env) error {
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

// Close stops the cleanup worker and waits for an in-flight pass to finish,
// so PostgreSQL and S3 (closed after Media) are never used by a stray pass.
// If ctx expires first, Close returns ctx.Err(); the pass still ends within
// the cleanup batch timeout. Repeated calls return nil.
func (c *Component) Close(ctx context.Context) error {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.service, c.cancel, c.done, c.closed = nil, nil, nil, true
	c.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
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
