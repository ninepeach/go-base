// Package mailer adapts net/smtp to go-base lifecycle management.
// It follows the provider lifecycle contract documented in package postgres.
package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"time"

	base "github.com/ninepeach/go-base/core"
)

var key = base.NewKey("mailer")

// Config configures an SMTP submission endpoint. Passwords are never logged.
type Config struct {
	Address    string
	Username   string
	Password   string
	From       string
	ServerName string
	// Timeout bounds one whole Send (dial, TLS, auth and data). Zero means 30 seconds.
	Timeout time.Duration
	// TLSConfig overrides the STARTTLS configuration, mainly for private CAs and tests.
	// ServerName and MinVersion are filled in when unset.
	TLSConfig *tls.Config
}

// Client owns no persistent network connection; each Send dials independently.
type Client struct {
	mu       sync.RWMutex
	cfg      Config
	fromFile bool
	started  bool
	closed   bool
}

// fileConfig is the shape of components.mailer.<instance> in the config file.
// timeout is a Go duration string such as "30s". TLSConfig cannot be set from
// the file; use RegisterConfig when a private CA or custom TLS is required.
type fileConfig struct {
	Address    string `conf:"address"`
	Username   string `conf:"username"`
	Password   string `conf:"password"`
	From       string `conf:"from"`
	ServerName string `conf:"server_name"`
	Timeout    string `conf:"timeout"`
}

// Register registers an instance configured by the components.mailer.<name>
// section of the Runtime config file. The section is decoded during Start;
// a missing section or unknown key fails startup.
func Register(name string, opts ...base.RegistrationOption) base.Registration {
	return base.Register(key, name, func() (base.Component, error) {
		return &Client{fromFile: true}, nil
	}, opts...)
}

// RegisterConfig registers an instance with an explicit Config. The config
// file is not consulted for this instance.
func RegisterConfig(name string, cfg Config, opts ...base.RegistrationOption) base.Registration {
	return base.Register(key, name, func() (base.Component, error) {
		return &Client{cfg: cfg}, nil
	}, opts...)
}

func loadConfig(env base.Env) (Config, error) {
	if env == nil {
		return Config{}, errors.New("mailer: config: nil env")
	}
	var fc fileConfig
	if err := env.Config().Decode("", &fc); err != nil {
		return Config{}, fmt.Errorf("mailer: config: %w", err)
	}
	cfg := Config{Address: fc.Address, Username: fc.Username, Password: fc.Password, From: fc.From, ServerName: fc.ServerName}
	if fc.Timeout != "" {
		d, err := time.ParseDuration(fc.Timeout)
		if err != nil {
			return Config{}, errors.New("mailer: config: timeout is not a valid duration")
		}
		cfg.Timeout = d
	}
	return cfg, nil
}

func resolve(component base.Component, err error) (*Client, bool, error) {
	if errors.Is(err, base.ErrNotRegistered) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	client, ok := component.(*Client)
	if !ok {
		return nil, false, errors.New("mailer: unexpected component type")
	}
	return client, true, nil
}

func Lookup(rt *base.Runtime, name string) (*Client, bool, error) {
	return resolve(rt.Get(key, name))
}

// LookupEnv resolves an already-started provider during another component's Start.
func LookupEnv(env base.Env, name string) (*Client, bool, error) {
	return resolve(env.Lookup(key, name))
}

func From(rt *base.Runtime, name string) *Client {
	c, ok, err := Lookup(rt, name)
	if err != nil {
		panic(err)
	}
	if !ok {
		panic("mailer: component not registered: " + name)
	}
	return c
}

// Start validates configuration only; SMTP servers are contacted per Send.
func (c *Client) Start(_ context.Context, env base.Env) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return base.ErrClosed
	}
	if c.fromFile {
		cfg, err := loadConfig(env)
		if err != nil {
			return err
		}
		c.cfg = cfg
	}
	if c.cfg.Address == "" || c.cfg.From == "" {
		return errors.New("mailer: address and sender required")
	}
	if strings.ContainsAny(c.cfg.From, "\r\n") {
		return errors.New("mailer: invalid sender")
	}
	if c.cfg.Timeout < 0 {
		return errors.New("mailer: negative timeout")
	}
	if c.cfg.Timeout == 0 {
		c.cfg.Timeout = 30 * time.Second
	}
	host, _, err := net.SplitHostPort(c.cfg.Address)
	if err != nil {
		return fmt.Errorf("mailer: invalid address: %w", err)
	}
	if c.cfg.ServerName == "" {
		c.cfg.ServerName = host
	}
	var tc *tls.Config
	if c.cfg.TLSConfig != nil {
		tc = c.cfg.TLSConfig.Clone()
	} else {
		tc = &tls.Config{}
	}
	if tc.ServerName == "" {
		tc.ServerName = c.cfg.ServerName
	}
	if tc.MinVersion == 0 {
		tc.MinVersion = tls.VersionTLS12
	}
	c.cfg.TLSConfig = tc
	c.started = true
	return nil
}

func (c *Client) Health(ctx context.Context) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed {
		return base.ErrClosed
	}
	if !c.started {
		return base.ErrNotRunning
	}
	return ctx.Err()
}

// Close rejects new sends. Sends already in flight are not interrupted; they
// remain bounded by their context and Config.Timeout.
func (c *Client) Close(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

// Send submits a fully composed RFC 5322 message over mandatory STARTTLS.
// The caller is responsible for headers, MIME encoding, and recipient validation.
// Cancelling ctx aborts blocked network I/O immediately.
func (c *Client) Send(ctx context.Context, recipients []string, message []byte) (err error) {
	c.mu.RLock()
	closed, started, cfg := c.closed, c.started, c.cfg
	c.mu.RUnlock()
	if closed {
		return base.ErrClosed
	}
	if !started {
		return base.ErrNotRunning
	}
	if len(recipients) == 0 {
		return errors.New("mailer: no recipients")
	}
	for _, recipient := range recipients {
		if recipient == "" || strings.ContainsAny(recipient, "\r\n") {
			return errors.New("mailer: invalid recipient")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", cfg.Address)
	if err != nil {
		return fmt.Errorf("mailer: connect: %w", err)
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	// Unblock any pending read or write as soon as ctx is cancelled.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	defer func() {
		if err != nil && ctx.Err() != nil {
			err = fmt.Errorf("mailer: %w: %w", ctx.Err(), err)
		}
	}()
	client, err := smtp.NewClient(conn, cfg.ServerName)
	if err != nil {
		return fmt.Errorf("mailer: smtp handshake: %w", err)
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); !ok {
		return errors.New("mailer: server does not support STARTTLS")
	}
	if err := client.StartTLS(cfg.TLSConfig); err != nil {
		return fmt.Errorf("mailer: tls: %w", err)
	}
	if cfg.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.ServerName)); err != nil {
			return fmt.Errorf("mailer: auth: %w", err)
		}
	}
	if err := client.Mail(cfg.From); err != nil {
		return fmt.Errorf("mailer: mail from: %w", err)
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("mailer: rcpt: %w", err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("mailer: data: %w", err)
	}
	if _, err := writer.Write(message); err != nil {
		_ = writer.Close()
		return fmt.Errorf("mailer: write: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("mailer: data close: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("mailer: quit: %w", err)
	}
	return nil
}
