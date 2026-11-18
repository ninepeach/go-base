// Package postgres adapts pgxpool to the go-base component lifecycle.
//
// Lifecycle contract (shared by all go-base providers):
//   - Start validates configuration, connects and verifies the connection.
//     On failure every partially created resource is released.
//   - Health returns base.ErrNotRunning before Start and base.ErrClosed after Close.
//   - Close is idempotent; repeated calls return nil.
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
	mu     sync.RWMutex
	cfg    Config
	pool   *pgxpool.Pool
	closed bool
}

func Register(name string, cfg Config, opts ...base.RegistrationOption) base.Registration {
	return base.Register(key, name, func() (base.Component, error) {
		return &Component{cfg: cfg}, nil
	}, opts...)
}

func (c *Component) Start(ctx context.Context, _ base.Env) error {
	if c.cfg.DSN == "" {
		return errors.New("postgres: empty DSN")
	}
	config, err := pgxpool.ParseConfig(c.cfg.DSN)
	if err != nil {
		// The parser error may echo the DSN, which can contain a password.
		return errors.New("postgres: invalid connection configuration")
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("postgres: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return fmt.Errorf("postgres: ping: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		pool.Close()
		return base.ErrClosed
	}
	c.pool = pool
	return nil
}

// Pool returns nil before Start and after Close.
func (c *Component) Pool() *pgxpool.Pool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.pool
}

func (c *Component) Health(ctx context.Context) error {
	c.mu.RLock()
	pool, closed := c.pool, c.closed
	c.mu.RUnlock()
	if closed {
		return base.ErrClosed
	}
	if pool == nil {
		return base.ErrNotRunning
	}
	return pool.Ping(ctx)
}

// Close waits for acquired connections to be released, as pgxpool.Close does.
func (c *Component) Close(context.Context) error {
	c.mu.Lock()
	pool := c.pool
	c.pool, c.closed = nil, true
	c.mu.Unlock()
	if pool != nil {
		pool.Close()
	}
	return nil
}

func resolve(component base.Component, err error) (*pgxpool.Pool, bool, error) {
	if errors.Is(err, base.ErrNotRegistered) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	c, ok := component.(*Component)
	if !ok {
		return nil, false, errors.New("postgres: unexpected component type")
	}
	pool := c.Pool()
	if pool == nil {
		return nil, false, base.ErrNotRunning
	}
	return pool, true, nil
}

// Lookup resolves a running pool from the Runtime.
func Lookup(rt *base.Runtime, name string) (*pgxpool.Pool, bool, error) {
	return resolve(rt.Get(key, name))
}

// LookupEnv resolves an already-started provider during another component's Start.
func LookupEnv(env base.Env, name string) (*pgxpool.Pool, bool, error) {
	return resolve(env.Lookup(key, name))
}

// From panics if the pool is missing; a missing provider is a programming error.
func From(rt *base.Runtime, name string) *pgxpool.Pool {
	pool, ok, err := Lookup(rt, name)
	if err != nil {
		panic(err)
	}
	if !ok {
		panic("postgres: component not registered: " + name)
	}
	return pool
}
