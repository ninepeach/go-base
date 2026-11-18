package media

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	base "github.com/ninepeach/go-base/core"
	postgres "github.com/ninepeach/go-base/postgres"
	s3 "github.com/ninepeach/go-base/s3"
)

var key = base.NewKey("media")

// Component resolves dependencies during Start and owns the reconciliation worker.
// It does not own the underlying PostgreSQL or S3 connections.
type Component struct {
	cfg Config
	mu sync.RWMutex
	service *Service
	cancel context.CancelFunc
	done chan struct{}
}

func Register(name string, cfg Config, opts ...base.RegistrationOption) base.Registration {
	return base.Register(key, name, func() (base.Component, error) {
		return &Component{cfg: cfg}, nil
	}, opts...)
}

func (c *Component) Start(ctx context.Context, env base.Env) error {
	db, ok, err := postgres.LookupEnv(env, c.cfg.PostgresInstance)
	if err != nil { return err }
	if !ok { return fmt.Errorf("media: missing postgres dependency %q: %w", c.cfg.PostgresInstance, base.ErrNotRegistered) }
	store, ok, err := s3.LookupEnv(env, c.cfg.S3Instance)
	if err != nil { return err }
	if !ok { return fmt.Errorf("media: missing s3 dependency %q: %w", c.cfg.S3Instance, base.ErrNotRegistered) }
	service, err := New(db, store, c.cfg)
	if err != nil { return err }
	if err := db.Ping(ctx); err != nil { return fmt.Errorf("media: postgres health: %w", err) }
	workerCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.mu.Lock()
	c.service, c.cancel, c.done = service, cancel, done
	c.mu.Unlock()
	go func() {
		defer close(done)
		interval := c.cfg.CleanupInterval
		if interval <= 0 { interval = time.Minute }
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done(): return
			case <-ticker.C:
				cleanupCtx, stop := context.WithTimeout(workerCtx, 30*time.Second)
				if err := service.Cleanup(cleanupCtx); err != nil && cleanupCtx.Err() == nil { env.Logger().Warn("media cleanup failed", "error", err) }
				stop()
			}
		}
	}()
	return nil
}

func (c *Component) Service() *Service {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.service
}

func (c *Component) Health(ctx context.Context) error {
	service := c.Service()
	if service == nil { return base.ErrNotRunning }
	return service.db.Ping(ctx)
}

func (c *Component) Close(ctx context.Context) error {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.service, c.cancel, c.done = nil, nil, nil
	c.mu.Unlock()
	if cancel == nil { return nil }
	cancel()
	select {
	case <-done: return nil
	case <-ctx.Done(): return ctx.Err()
	}
}

func Lookup(rt *base.Runtime, name string) (*Service, bool, error) {
	component, err := rt.Get(key, name)
	if errors.Is(err, base.ErrNotRegistered) { return nil, false, nil }
	if err != nil { return nil, false, err }
	c, ok := component.(*Component)
	if !ok { return nil, false, errors.New("media: unexpected component type") }
	service := c.Service()
	if service == nil { return nil, false, base.ErrNotRunning }
	return service, true, nil
}

func FromComponent(rt *base.Runtime, name string) (*Service, error) {
	service, ok, err := Lookup(rt, name)
	if err != nil { return nil, err }
	if !ok { return nil, base.ErrNotRegistered }
	return service, nil
}

// Cleanup reconciles expired pending and deleting records. Pending objects
// are deleted rather than finalized, since their completeness is unknown.
// The query claims a row by updating its timestamp; repeated runs are safe.
func (s *Service) Cleanup(ctx context.Context) error {
	age := s.cfg.StaleAfter
	if age <= 0 { age = 15*time.Minute }
	rows, err := s.db.Query(ctx, fmt.Sprintf(
		"SELECT id, state FROM %s WHERE state IN ('pending','deleting') AND updated_at < now() - ($1 * interval '1 second') ORDER BY updated_at LIMIT 100", s.table()),
		age.Seconds())
	if err != nil { return fmt.Errorf("media: list cleanup candidates: %w", err) }
	type item struct { id uuid.UUID; state string }
	var items []item
	for rows.Next() {
		var item item
		if err := rows.Scan(&item.id, &item.state); err != nil { rows.Close(); return err }
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil { return err }
	var failures []error
	for _, item := range items {
		if err := s.cleanupOne(ctx, item.id, item.state, age); err != nil {
			failures = append(failures, fmt.Errorf("media: cleanup %s: %w", item.id, err))
		}
	}
	return errors.Join(failures...)
}

func (s *Service) cleanupOne(ctx context.Context, id uuid.UUID, state string, age time.Duration) error {
	var bucket, key string
	err := s.db.QueryRow(ctx, fmt.Sprintf(
		"UPDATE %s SET state='deleting', updated_at=now() WHERE id=$1 AND state=$2 AND updated_at < now() - ($3 * interval '1 second') RETURNING bucket, object_key", s.table()),
		id, state, age.Seconds()).Scan(&bucket, &key)
	if errors.Is(err, pgx.ErrNoRows) { return nil }
	if err != nil { return err }
	return s.removeObjectAndRow(ctx, id, bucket, key)
}
