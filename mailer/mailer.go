// Package mailer adapts net/smtp to go-base lifecycle management.
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
	Address string
	Username string
	Password string
	From string
	ServerName string
	Timeout time.Duration
}

// Client owns no persistent network connection; each Send dials independently.
type Client struct {
	mu sync.RWMutex
	cfg Config
	closed bool
}

func Register(name string, cfg Config, opts ...base.RegistrationOption) base.Registration {
	return base.Register(key, name, func() (base.Component, error) {
		return &Client{cfg: cfg}, nil
	}, opts...)
}

func Lookup(rt *base.Runtime, name string) (*Client, bool, error) {
	c, err := rt.Get(key, name)
	if errors.Is(err, base.ErrNotRegistered) { return nil, false, nil }
	if err != nil { return nil, false, err }
	client, ok := c.(*Client)
	if !ok { return nil, false, errors.New("mailer: unexpected component type") }
	return client, true, nil
}

func From(rt *base.Runtime, name string) *Client {
	c, ok, err := Lookup(rt, name)
	if err != nil { panic(err) }
	if !ok { panic("mailer: component not registered: " + name) }
	return c
}

func (c *Client) Start(_ context.Context, _ base.Env) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cfg.Address == "" || c.cfg.From == "" {
		return errors.New("mailer: address and sender required")
	}
	if c.cfg.Timeout <= 0 { c.cfg.Timeout = 10 * time.Second }
	if c.cfg.ServerName == "" {
		host, _, err := net.SplitHostPort(c.cfg.Address)
		if err != nil { return fmt.Errorf("mailer: invalid address: %w", err) }
		c.cfg.ServerName = host
	}
	return nil
}
func (c *Client) Health(ctx context.Context) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed { return base.ErrClosed }
	return ctx.Err()
}
func (c *Client) Close(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

// Send submits a fully composed RFC 5322 message over explicit STARTTLS.
// The caller is responsible for headers, MIME encoding, and recipient validation.
func (c *Client) Send(ctx context.Context, recipients []string, message []byte) error {
	c.mu.RLock()
	if c.closed { c.mu.RUnlock(); return base.ErrClosed }
	cfg := c.cfg
	c.mu.RUnlock()
	if len(recipients) == 0 { return errors.New("mailer: no recipients") }
	if err := ctx.Err(); err != nil { return err }
	dialer := &net.Dialer{Timeout: cfg.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", cfg.Address)
	if err != nil { return fmt.Errorf("mailer: connect: %w", err) }
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok { _ = conn.SetDeadline(deadline) } else { _ = conn.SetDeadline(time.Now().Add(cfg.Timeout)) }
	client, err := smtp.NewClient(conn, cfg.ServerName)
	if err != nil { return fmt.Errorf("mailer: smtp handshake: %w", err) }
	defer client.Close()
	ok, _ := client.Extension("STARTTLS")
	if !ok { return errors.New("mailer: server does not support STARTTLS") }
	if err := client.StartTLS(&tls.Config{ServerName: cfg.ServerName, MinVersion: tls.VersionTLS12}); err != nil { return fmt.Errorf("mailer: tls: %w", err) }
	if cfg.Username != "" {
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.ServerName)
		if err := client.Auth(auth); err != nil { return fmt.Errorf("mailer: auth: %w", err) }
	}
	if err := client.Mail(cfg.From); err != nil { return err }
	for _, recipient := range recipients {
		if strings.ContainsAny(recipient, "\r\n") { return errors.New("mailer: invalid recipient") }
		if err := client.Rcpt(recipient); err != nil { return err }
	}
	writer, err := client.Data()
	if err != nil { return err }
	if _, err := writer.Write(message); err != nil { _ = writer.Close(); return err }
	if err := writer.Close(); err != nil { return err }
	return client.Quit()
}
