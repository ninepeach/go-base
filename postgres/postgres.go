// Package postgres adapts pgxpool to the go-base component lifecycle.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	base "github.com/ninepeach/go-base/core"
)

var key = base.NewKey("postgres")

type Config struct {
	DSN string
}

type Component struct {
	mu sync.RWMutex
	cfg Config
	pool *pgxpool.Pool
}

func Register(name string, cfg Config, opts ...base.RegistrationOption) base.Registration {
	return base.Register(key, name, func() (base.Component, error) {
		return &Component{cfg: cfg}, nil
	}, opts...)
}

func (c *Component) Start(ctx context.Context, _ base.Env) error {
	if c.cfg.DSN == "" { return errors.New("postgres: empty DSN") }
	config, err := pgxpool.ParseConfig(c.cfg.DSN)
	if err != nil { return errors.New("postgres: invalid connection configuration") }
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil { return fmt.Errorf("postgres: create pool: %w", err) }
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return fmt.Errorf("postgres: ping: %w", err)
	}
	c.mu.Lock()
	c.pool = pool
	c.mu.Unlock()
	return nil
}

func (c *Component) Pool() *pgxpool.Pool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.pool
}

func (c *Component) Health(ctx context.Context) error {
	pool := c.Pool()
	if pool == nil { return base.ErrNotRunning }
	return pool.Ping(ctx)
}

func (c *Component) Close(context.Context) error {
	c.mu.Lock()
	pool := c.pool
	c.pool = nil
	c.mu.Unlock()
	if pool != nil { pool.Close() }
	return nil
}

func Lookup(rt *base.Runtime, name string) (*pgxpool.Pool, bool, error) {
	component, err := rt.Get(key, name)
	if errors.Is(err, base.ErrNotRegistered) { return nil, false, nil }
	if err != nil { return nil, false, err }
	c, ok := component.(*Component)
	if !ok { return nil, false, errors.New("postgres: unexpected component type") }
	pool := c.Pool()
	if pool == nil { return nil, false, base.ErrNotRunning }
	return pool, true, nil
}

// LookupEnv resolves an already-started provider during another component's Start.
func LookupEnv(env base.Env, name string) (*pgxpool.Pool, bool, error) {
	component, err := env.Lookup(key, name)
	if errors.Is(err, base.ErrNotRegistered) { return nil, false, nil }
	if err != nil { return nil, false, err }
	c, ok := component.(*Component)
	if !ok { return nil, false, errors.New("postgres: unexpected component type") }
	pool := c.Pool()
	if pool == nil { return nil, false, base.ErrNotRunning }
	return pool, true, nil
}

func From(rt *base.Runtime, name string) *pgxpool.Pool {
	pool, ok, err := Lookup(rt, name)
	if err != nil { panic(err) }
	if !ok { panic("postgres: component not registered: " + name) }
	return pool
}
