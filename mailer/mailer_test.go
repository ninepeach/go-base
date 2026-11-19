package mailer

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	base "github.com/ninepeach/go-base/core"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func testCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

type smtpServer struct {
	ln       net.Listener
	cert     tls.Certificate
	starttls bool
	stall    bool
	failQuit bool // drop the connection instead of answering QUIT
	mu       sync.Mutex
	messages []string
	wg       sync.WaitGroup
}

func newSMTPServer(t *testing.T, starttls, stall bool) (*smtpServer, *x509.CertPool) {
	t.Helper()
	cert, pool := testCert(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &smtpServer{ln: ln, cert: cert, starttls: starttls, stall: stall}
	s.wg.Add(1)
	go s.accept()
	t.Cleanup(func() { _ = ln.Close(); s.wg.Wait() })
	return s, pool
}

func (s *smtpServer) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			s.session(conn)
		}()
	}
}

func (s *smtpServer) session(conn net.Conn) {
	if s.stall {
		_, _ = bufio.NewReader(conn).ReadByte() // never greet; wait for the client to give up
		return
	}
	r, w := bufio.NewReader(conn), conn
	write := func(line string) { _, _ = w.Write([]byte(line + "\r\n")) }
	write("220 test ESMTP")
	secure := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			if s.starttls && !secure {
				write("250-test")
				write("250 STARTTLS")
			} else {
				write("250-test")
				write("250 AUTH PLAIN")
			}
		case cmd == "STARTTLS":
			write("220 ready")
			tc := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{s.cert}})
			if err := tc.Handshake(); err != nil {
				return
			}
			r, w, secure = bufio.NewReader(tc), tc, true
		case strings.HasPrefix(cmd, "AUTH"):
			write("235 ok")
		case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
			write("250 ok")
		case cmd == "DATA":
			write("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			s.mu.Lock()
			s.messages = append(s.messages, b.String())
			s.mu.Unlock()
			write("250 queued")
		case cmd == "QUIT":
			if !s.failQuit {
				write("221 bye")
			}
			return
		default:
			write("502 unknown")
		}
	}
}

func started(t *testing.T, addr string, pool *x509.CertPool) *Client {
	t.Helper()
	c := &Client{cfg: Config{Address: addr, From: "app@example.com", Username: "u", Password: "p", Timeout: 5 * time.Second,
		TLSConfig: &tls.Config{RootCAs: pool}}}
	if err := c.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSendOverSTARTTLS(t *testing.T) {
	srv, pool := newSMTPServer(t, true, false)
	c := started(t, srv.ln.Addr().String(), pool)
	msg := "Subject: hi\r\n\r\nhello\r\n"
	if err := c.Send(context.Background(), []string{"user@example.com"}, []byte(msg)); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.messages) != 1 || !strings.Contains(srv.messages[0], "hello") {
		t.Fatalf("messages: %q", srv.messages)
	}
}

func TestSendRefusesServerWithoutSTARTTLS(t *testing.T) {
	srv, pool := newSMTPServer(t, false, false)
	c := started(t, srv.ln.Addr().String(), pool)
	if err := c.Send(context.Background(), []string{"user@example.com"}, []byte("x")); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("want STARTTLS error, got %v", err)
	}
}

func TestSendCancellationUnblocksIO(t *testing.T) {
	srv, pool := newSMTPServer(t, true, true)
	c := started(t, srv.ln.Addr().String(), pool)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	err := c.Send(ctx, []string{"user@example.com"}, []byte("x"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("cancellation took %v", time.Since(start))
	}
}

func TestSendTimeoutBoundsStalledServer(t *testing.T) {
	srv, pool := newSMTPServer(t, true, true)
	c := started(t, srv.ln.Addr().String(), pool)
	c.cfg.Timeout = 300 * time.Millisecond
	err := c.Send(context.Background(), []string{"user@example.com"}, []byte("x"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
}

// TestClassifyTimeout pins the race behind a former flake: the socket
// deadline (equal to ctx's) fired before ctx's timer, and Send returned a
// bare I/O timeout instead of context.DeadlineExceeded.
func TestClassifyTimeout(t *testing.T) {
	live := context.Background()
	ioTimeout := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
	if err := classifyTimeout(live, fmt.Errorf("smtp handshake: %w", ioTimeout)); !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("socket deadline before ctx timer: %v", err)
	}
	canceled, cancel := context.WithCancel(live)
	cancel()
	if err := classifyTimeout(canceled, ioTimeout); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ctx: %v", err)
	}
	other := errors.New("550 rejected")
	if err := classifyTimeout(live, other); err != other {
		t.Fatalf("unrelated error changed: %v", err)
	}
	if classifyTimeout(canceled, nil) != nil {
		t.Fatal("nil error changed")
	}
}

func TestValidationAndLifecycle(t *testing.T) {
	ctx := context.Background()
	for _, cfg := range []Config{
		{},
		{Address: "127.0.0.1:25", From: "a@b\r\nBcc: x@y"},
		{Address: "no-port", From: "a@b"},
		{Address: "127.0.0.1:25", From: "a@b", Timeout: -1},
	} {
		if err := (&Client{cfg: cfg}).Start(ctx, nil); err == nil {
			t.Fatalf("accepted invalid config %+v", cfg)
		}
	}
	c := &Client{cfg: Config{Address: "127.0.0.1:25", From: "a@b"}}
	if err := c.Send(ctx, []string{"x@y"}, nil); !errors.Is(err, base.ErrNotRunning) {
		t.Fatalf("send before start: %v", err)
	}
	if err := c.Health(ctx); !errors.Is(err, base.ErrNotRunning) {
		t.Fatalf("health before start: %v", err)
	}
	if err := c.Start(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Send(ctx, []string{"x@y\r\nRCPT TO:<evil>"}, nil); err == nil {
		t.Fatal("recipient with CRLF must be rejected")
	}
	if err := c.Send(ctx, nil, nil); err == nil {
		t.Fatal("no recipients must be rejected")
	}
	_ = c.Close(ctx)
	_ = c.Close(ctx)
	if err := c.Send(ctx, []string{"x@y"}, nil); !errors.Is(err, base.ErrClosed) {
		t.Fatalf("send after close: %v", err)
	}
	if err := c.Health(ctx); !errors.Is(err, base.ErrClosed) {
		t.Fatalf("health after close: %v", err)
	}
}

func TestSendSucceedsWhenQuitFailsAfterAcceptedData(t *testing.T) {
	srv, pool := newSMTPServer(t, true, false)
	srv.failQuit = true
	c := started(t, srv.ln.Addr().String(), pool)
	if err := c.Send(context.Background(), []string{"user@example.com"}, []byte("Subject: x\r\n\r\nbody\r\n")); err != nil {
		t.Fatalf("accepted message reported as failed (callers would resend): %v", err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.messages) != 1 {
		t.Fatalf("messages: %d", len(srv.messages))
	}
}
