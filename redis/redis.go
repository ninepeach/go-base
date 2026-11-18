// Package redis adapts go-redis to the go-base component lifecycle.
// It follows the provider lifecycle contract documented in package postgres.
package redis

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	base "github.com/ninepeach/go-base/core"
	goredis "github.com/redis/go-redis/v9"
)

var key = base.NewKey("redis")

type Config struct {
	Address  string
	Username string
	Password string
	DB       int
	// DialTimeout bounds each connection attempt. Zero means 5 seconds.
	DialTimeout time.Duration
}

// fileConfig is the shape of components.redis.<instance> in the config file.
// dial_timeout is a Go duration string such as "5s".
type fileConfig struct {
	Address     string `conf:"address"`
	Username    string `conf:"username"`
	Password    string `conf:"password"`
	DB          int    `conf:"db"`
	DialTimeout string `conf:"dial_timeout"`
}

type Component struct {
	mu       sync.RWMutex
	cfg      Config
	fromFile bool
	client   *goredis.Client
	closed   bool
}

// Register registers an instance configured by the components.redis.<name>
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
		return Config{}, errors.New("redis: config: nil env")
	}
	var fc fileConfig
	if err := env.Config().Decode("", &fc); err != nil {
		return Config{}, fmt.Errorf("redis: config: %w", err)
	}
	cfg := Config{Address: fc.Address, Username: fc.Username, Password: fc.Password, DB: fc.DB}
	if fc.DialTimeout != "" {
		d, err := time.ParseDuration(fc.DialTimeout)
		if err != nil {
			return Config{}, errors.New("redis: config: dial_timeout is not a valid duration")
		}
		cfg.DialTimeout = d
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
	if c.cfg.Address == "" {
		return errors.New("redis: empty address")
	}
	if c.cfg.DB < 0 {
		return errors.New("redis: negative database index")
	}
	if c.cfg.DialTimeout < 0 {
		return errors.New("redis: negative dial timeout")
	}
	dial := c.cfg.DialTimeout
	if dial == 0 {
		dial = 5 * time.Second
	}
	client := goredis.NewClient(&goredis.Options{
		Addr:        c.cfg.Address,
		Username:    c.cfg.Username,
		Password:    c.cfg.Password,
		DB:          c.cfg.DB,
		DialTimeout: dial,
	})
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return fmt.Errorf("redis: ping: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		_ = client.Close()
		return base.ErrClosed
	}
	c.client = client
	return nil
}

// Client returns nil before Start and after Close.
func (c *Component) Client() *goredis.Client {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client
}

func (c *Component) Health(ctx context.Context) error {
	c.mu.RLock()
	client, closed := c.client, c.closed
	c.mu.RUnlock()
	if closed {
		return base.ErrClosed
	}
	if client == nil {
		return base.ErrNotRunning
	}
	return client.Ping(ctx).Err()
}

func (c *Component) Close(context.Context) error {
	c.mu.Lock()
	client := c.client
	c.client, c.closed = nil, true
	c.mu.Unlock()
	if client != nil {
		if err := client.Close(); err != nil {
			return fmt.Errorf("redis: close: %w", err)
		}
	}
	return nil
}

func resolve(component base.Component, err error) (*goredis.Client, bool, error) {
	if errors.Is(err, base.ErrNotRegistered) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	c, ok := component.(*Component)
	if !ok {
		return nil, false, errors.New("redis: unexpected component type")
	}
	client := c.Client()
	if client == nil {
		return nil, false, base.ErrNotRunning
	}
	return client, true, nil
}

func Lookup(rt *base.Runtime, name string) (*goredis.Client, bool, error) {
	return resolve(rt.Get(key, name))
}

// LookupEnv resolves an already-started provider during another component's Start.
func LookupEnv(env base.Env, name string) (*goredis.Client, bool, error) {
	return resolve(env.Lookup(key, name))
}

func From(rt *base.Runtime, name string) *goredis.Client {
	client, ok, err := Lookup(rt, name)
	if err != nil {
		panic(err)
	}
	if !ok {
		panic("redis: component not registered: " + name)
	}
	return client
}
