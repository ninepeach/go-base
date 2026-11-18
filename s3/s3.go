// Package s3 adapts an S3-compatible MinIO client to go-base lifecycle.
// It follows the provider lifecycle contract documented in package postgres.
//
// minio.New performs no network I/O, so only a HealthBucket check can detect
// a wrong endpoint or invalid credentials. HealthBucket is therefore required:
// Start fails without it, and fails fast when the bucket is unreachable or
// missing. Local development can opt out with AllowNoHealthBucket.
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

// ErrBucketMissing reports that a configured bucket does not exist.
var ErrBucketMissing = errors.New("s3: bucket does not exist")

type Config struct {
	Endpoint     string
	AccessKey    string
	SecretKey    string
	SessionToken string
	UseTLS       bool
	Region       string
	// HealthBucket is checked by Start and Health. Required unless AllowNoHealthBucket.
	HealthBucket string
	// AllowNoHealthBucket permits an empty HealthBucket. Development only:
	// connection and credential errors then surface on first use.
	AllowNoHealthBucket bool
}

// ErrHealthBucketRequired is returned by Start when HealthBucket is empty and
// AllowNoHealthBucket is false.
var ErrHealthBucketRequired = errors.New("s3: HealthBucket is required (set AllowNoHealthBucket only for development)")

// fileConfig is the shape of components.s3.<instance> in the config file.
type fileConfig struct {
	Endpoint            string `conf:"endpoint"`
	AccessKey           string `conf:"access_key"`
	SecretKey           string `conf:"secret_key"`
	SessionToken        string `conf:"session_token"`
	UseTLS              bool   `conf:"use_tls"`
	Region              string `conf:"region"`
	HealthBucket        string `conf:"health_bucket"`
	AllowNoHealthBucket bool   `conf:"allow_no_health_bucket"`
}

type Component struct {
	mu       sync.RWMutex
	cfg      Config
	fromFile bool
	client   *minio.Client
	closed   bool
}

// Register registers an instance configured by the components.s3.<name>
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
		return Config{}, errors.New("s3: config: nil env")
	}
	var fc fileConfig
	if err := env.Config().Decode("", &fc); err != nil {
		return Config{}, fmt.Errorf("s3: config: %w", err)
	}
	return Config(fc), nil
}

func (c *Component) Start(ctx context.Context, env base.Env) error {
	if c.fromFile {
		cfg, err := loadConfig(env)
		if err != nil {
			return err
		}
		c.cfg = cfg
	}
	if c.cfg.Endpoint == "" {
		return errors.New("s3: empty endpoint")
	}
	if c.cfg.HealthBucket == "" && !c.cfg.AllowNoHealthBucket {
		return ErrHealthBucketRequired
	}
	if (c.cfg.AccessKey == "") != (c.cfg.SecretKey == "") {
		return errors.New("s3: access key and secret key must be set together")
	}
	client, err := minio.New(c.cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(c.cfg.AccessKey, c.cfg.SecretKey, c.cfg.SessionToken),
		Secure: c.cfg.UseTLS,
		Region: c.cfg.Region,
	})
	if err != nil {
		return fmt.Errorf("s3: initialize client: %w", err)
	}
	if c.cfg.HealthBucket != "" {
		if err := checkBucket(ctx, client, c.cfg.HealthBucket); err != nil {
			return err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return base.ErrClosed
	}
	c.client = client
	return nil
}

func checkBucket(ctx context.Context, client *minio.Client, bucket string) error {
	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		return fmt.Errorf("s3: bucket check: %w", err)
	}
	if !exists {
		return fmt.Errorf("%w: %s", ErrBucketMissing, bucket)
	}
	return nil
}

// Client returns nil before Start and after Close.
func (c *Component) Client() *minio.Client {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client
}

// Health probes HealthBucket when configured; otherwise it only reports state.
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
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.cfg.HealthBucket == "" {
		return nil
	}
	return checkBucket(ctx, client, c.cfg.HealthBucket)
}

// Close releases the client reference; minio clients hold no closable resources.
func (c *Component) Close(context.Context) error {
	c.mu.Lock()
	c.client, c.closed = nil, true
	c.mu.Unlock()
	return nil
}

func resolve(component base.Component, err error) (*minio.Client, bool, error) {
	if errors.Is(err, base.ErrNotRegistered) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	c, ok := component.(*Component)
	if !ok {
		return nil, false, errors.New("s3: unexpected component type")
	}
	client := c.Client()
	if client == nil {
		return nil, false, base.ErrNotRunning
	}
	return client, true, nil
}

func Lookup(rt *base.Runtime, name string) (*minio.Client, bool, error) {
	return resolve(rt.Get(key, name))
}

// LookupEnv resolves an already-started provider during another component's Start.
func LookupEnv(env base.Env, name string) (*minio.Client, bool, error) {
	return resolve(env.Lookup(key, name))
}

func From(rt *base.Runtime, name string) *minio.Client {
	client, ok, err := Lookup(rt, name)
	if err != nil {
		panic(err)
	}
	if !ok {
		panic("s3: component not registered: " + name)
	}
	return client
}
