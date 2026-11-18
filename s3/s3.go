// Package s3 adapts an S3-compatible MinIO client to go-base lifecycle.
package s3

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	base "github.com/ninepeach/go-base/core"
)

var key = base.NewKey("s3")

type Config struct {
	Endpoint string
	AccessKey string
	SecretKey string
	SessionToken string
	UseTLS bool
	HealthBucket string
}

type Component struct {
	mu sync.RWMutex
	cfg Config
	client *minio.Client
	closed bool
}

func Register(name string, cfg Config, opts ...base.RegistrationOption) base.Registration {
	return base.Register(key, name, func() (base.Component, error) {
		return &Component{cfg: cfg}, nil
	}, opts...)
}

func (c *Component) Start(ctx context.Context, _ base.Env) error {
	if c.cfg.Endpoint == "" { return errors.New("s3: empty endpoint") }
	client, err := minio.New(c.cfg.Endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(c.cfg.AccessKey, c.cfg.SecretKey, c.cfg.SessionToken),
		Secure: c.cfg.UseTLS,
	})
	if err != nil { return fmt.Errorf("s3: initialize client: %w", err) }
	if c.cfg.HealthBucket != "" {
		exists, err := client.BucketExists(ctx, c.cfg.HealthBucket)
		if err != nil { return fmt.Errorf("s3: bucket check: %w", err) }
		if !exists { return errors.New("s3: health bucket does not exist") }
	}
	c.mu.Lock()
	c.client = client
	c.closed = false
	c.mu.Unlock()
	return nil
}

func (c *Component) Client() *minio.Client {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed { return nil }
	return c.client
}

func (c *Component) Health(ctx context.Context) error {
	client := c.Client()
	if client == nil { return base.ErrNotRunning }
	if err := ctx.Err(); err != nil { return err }
	if c.cfg.HealthBucket == "" { return nil }
	exists, err := client.BucketExists(ctx, c.cfg.HealthBucket)
	if err != nil { return err }
	if !exists { return errors.New("s3: health bucket does not exist") }
	return nil
}

func (c *Component) Close(context.Context) error {
	c.mu.Lock()
	c.closed = true
	c.client = nil
	c.mu.Unlock()
	return nil
}

func Lookup(rt *base.Runtime, name string) (*minio.Client, bool, error) {
	component, err := rt.Get(key, name)
	if errors.Is(err, base.ErrNotRegistered) { return nil, false, nil }
	if err != nil { return nil, false, err }
	c, ok := component.(*Component)
	if !ok { return nil, false, errors.New("s3: unexpected component type") }
	client := c.Client()
	if client == nil { return nil, false, base.ErrNotRunning }
	return client, true, nil
}

// LookupEnv resolves an already-started provider during another component's Start.
func LookupEnv(env base.Env, name string) (*minio.Client, bool, error) {
	component, err := env.Lookup(key, name)
	if errors.Is(err, base.ErrNotRegistered) { return nil, false, nil }
	if err != nil { return nil, false, err }
	c, ok := component.(*Component)
	if !ok { return nil, false, errors.New("s3: unexpected component type") }
	client := c.Client()
	if client == nil { return nil, false, base.ErrNotRunning }
	return client, true, nil
}

func From(rt *base.Runtime, name string) *minio.Client {
	client, ok, err := Lookup(rt, name)
	if err != nil { panic(err) }
	if !ok { panic("s3: component not registered: " + name) }
	return client
}
