// Package redis adapts go-redis to the go-base component lifecycle.
package redis

import (
	"context"
	"errors"
	"fmt"
	"sync"

	base "github.com/ninepeach/go-base/core"
	goredis "github.com/redis/go-redis/v9"
)

var key = base.NewKey("redis")

type Config struct {
	Address string
	Username string
	Password string
	DB int
}

type Component struct {
	mu sync.RWMutex
	cfg Config
	client *goredis.Client
}

func Register(name string, cfg Config, opts ...base.RegistrationOption) base.Registration {
	return base.Register(key, name, func() (base.Component, error) {
		return &Component{cfg: cfg}, nil
	}, opts...)
}

func (c *Component) Start(ctx context.Context, _ base.Env) error {
	if c.cfg.Address == "" { return errors.New("redis: empty address") }
	client := goredis.NewClient(&goredis.Options{
		Addr: c.cfg.Address,
		Username: c.cfg.Username,
		Password: c.cfg.Password,
		DB: c.cfg.DB,
	})
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return fmt.Errorf("redis: ping: %w", err)
	}
	c.mu.Lock()
	c.client = client
	c.mu.Unlock()
	return nil
}

func (c *Component) Client() *goredis.Client {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client
}

func (c *Component) Health(ctx context.Context) error {
	client := c.Client()
	if client == nil { return base.ErrNotRunning }
	return client.Ping(ctx).Err()
}

func (c *Component) Close(context.Context) error {
	c.mu.Lock()
	client := c.client
	c.client = nil
	c.mu.Unlock()
	if client != nil { return client.Close() }
	return nil
}

func Lookup(rt *base.Runtime, name string) (*goredis.Client, bool, error) {
	component, err := rt.Get(key, name)
	if errors.Is(err, base.ErrNotRegistered) { return nil, false, nil }
	if err != nil { return nil, false, err }
	c, ok := component.(*Component)
	if !ok { return nil, false, errors.New("redis: unexpected component type") }
	client := c.Client()
	if client == nil { return nil, false, base.ErrNotRunning }
	return client, true, nil
}

func From(rt *base.Runtime, name string) *goredis.Client {
	client, ok, err := Lookup(rt, name)
	if err != nil { panic(err) }
	if !ok { panic("redis: component not registered: " + name) }
	return client
}
