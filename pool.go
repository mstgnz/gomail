package gomail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/textproto"
	"sync"
	"time"
)

var defaultPoolSize = DefaultPoolSize

var errPoolClosed = errors.New("gomail: pool is closed")

// Pool keeps up to size idle, authenticated SMTP connections for reuse. Connections are
// opened on demand, and an idle one is checked with NOOP before it is handed out.
// A Pool is safe for concurrent use.
type Pool struct {
	config *Mail
	size   int

	mu     sync.Mutex
	idle   []*poolConn
	closed bool
}

// poolConn keeps the raw connection next to the client: deadlines are set on the conn.
type poolConn struct {
	conn   net.Conn
	client *smtp.Client
}

func (pc *poolConn) close() {
	_ = pc.client.Close()
}

// NewPool creates a pool that keeps at most size idle connections (DefaultPoolSize when
// size <= 0). It opens no connection; the first send does.
func NewPool(config *Mail, size int) (*Pool, error) {
	if config == nil {
		return nil, errors.New("gomail: pool config is nil")
	}
	if size <= 0 {
		size = defaultPoolSize
	}
	return &Pool{config: config, size: size}, nil
}

// createConnection dials, greets, upgrades to TLS when configured and authenticates.
// Each step is bounded by the Mail's Timeout and by ctx.
func (p *Pool) createConnection(ctx context.Context) (*poolConn, error) {
	cfg := p.config
	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	dialer := &net.Dialer{
		Timeout:   cfg.getTimeout(),
		KeepAlive: cfg.getKeepAlive(),
	}

	var conn net.Conn
	var err error
	if cfg.tlsConfig != nil && !cfg.tlsConfig.StartTLS {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: cfg.stdTLSConfig()}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, err
	}

	release := guard(ctx, conn, stepDeadline(ctx, cfg.getTimeout()))
	client, err := p.handshake(conn)
	if !release() {
		if client != nil {
			_ = client.Close()
		} else {
			_ = conn.Close()
		}
		if err == nil {
			err = ctx.Err()
		}
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	return &poolConn{conn: conn, client: client}, nil
}

// handshake runs greeting, EHLO, STARTTLS and AUTH. It closes conn on failure.
func (p *Pool) handshake(conn net.Conn) (*smtp.Client, error) {
	cfg := p.config
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if cfg.LocalName != "" {
		if err := client.Hello(cfg.LocalName); err != nil {
			_ = client.Close()
			return nil, err
		}
	}
	if cfg.tlsConfig != nil && cfg.tlsConfig.StartTLS {
		if err := client.StartTLS(cfg.stdTLSConfig()); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("STARTTLS failed: %w", err)
		}
	}
	// PlainAuth refuses to send credentials over an unencrypted connection, except to localhost.
	if err := client.Auth(smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

// getConnection returns a live connection: the most recently used idle one that answers
// NOOP, or a new one. Dead idle connections are closed and skipped, never returned as errors.
func (p *Pool) getConnection(ctx context.Context) (*poolConn, error) {
	if p == nil || p.config == nil {
		return nil, errors.New("gomail: pool is not initialized")
	}
	for {
		pc, err := p.takeIdle()
		if err != nil {
			return nil, err
		}
		if pc == nil {
			break
		}
		if p.alive(ctx, pc) {
			return pc, nil
		}
		pc.close()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return p.createConnection(ctx)
}

func (p *Pool) takeIdle() (*poolConn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, errPoolClosed
	}
	n := len(p.idle)
	if n == 0 {
		return nil, nil
	}
	pc := p.idle[n-1]
	p.idle[n-1] = nil
	p.idle = p.idle[:n-1]
	return pc, nil
}

// alive probes pc with NOOP; servers drop idle sessions, and a silent peer must not hang the probe.
func (p *Pool) alive(ctx context.Context, pc *poolConn) bool {
	release := guard(ctx, pc.conn, stepDeadline(ctx, p.config.getTimeout()))
	err := pc.client.Noop()
	return release() && err == nil
}

// releaseConnection returns a healthy connection to the pool, or closes it when the pool is
// full or closed.
func (p *Pool) releaseConnection(pc *poolConn) {
	if pc == nil {
		return
	}
	p.mu.Lock()
	if !p.closed && len(p.idle) < p.size {
		p.idle = append(p.idle, pc)
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	pc.close()
}

// finish settles a connection after a transaction. A server reply error (4xx/5xx) leaves the
// session in sync, so RSET is tried and the connection kept if it succeeds. Any other error
// (I/O, deadline, a body cut off before the terminator) leaves the state unknown: close it.
func (p *Pool) finish(pc *poolConn, err error, resettable bool) {
	if err == nil {
		p.releaseConnection(pc)
		return
	}
	var reply *textproto.Error
	if !resettable || !errors.As(err, &reply) {
		pc.close()
		return
	}
	_ = pc.conn.SetDeadline(time.Now().Add(p.config.getTimeout()))
	if pc.client.Reset() != nil {
		pc.close()
		return
	}
	_ = pc.conn.SetDeadline(time.Time{})
	p.releaseConnection(pc)
}

// Close closes all idle connections. Connections in use are closed when they are released.
func (p *Pool) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	idle := p.idle
	p.idle = nil
	p.closed = true
	p.mu.Unlock()
	for _, pc := range idle {
		pc.close()
	}
}

// guard applies deadline d (zero means none) to conn and interrupts its I/O once ctx is done.
// The returned release detaches the guard; it reports false when ctx fired, which leaves
// conn with an expired deadline, so the caller must close it.
func guard(ctx context.Context, conn net.Conn, d time.Time) (release func() bool) {
	_ = conn.SetDeadline(d)
	stop := context.AfterFunc(ctx, func() {
		_ = conn.SetDeadline(time.Unix(1, 0))
	})
	return func() bool {
		if !stop() {
			return false
		}
		_ = conn.SetDeadline(time.Time{})
		return true
	}
}

// stepDeadline is now+timeout, or ctx's deadline when that comes first.
func stepDeadline(ctx context.Context, timeout time.Duration) time.Time {
	d := time.Now().Add(timeout)
	if cd, ok := ctx.Deadline(); ok && cd.Before(d) {
		return cd
	}
	return d
}

// withContext makes errors.Is(err, ctx.Err()) hold when ctx ended the operation.
func withContext(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	cerr := ctx.Err()
	if cerr == nil {
		// The conn deadline can fire a moment before ctx's own timer marks it done.
		if d, ok := ctx.Deadline(); ok && !time.Now().Before(d) {
			cerr = context.DeadlineExceeded
		}
	}
	if cerr == nil || errors.Is(err, cerr) {
		return err
	}
	return fmt.Errorf("%w: %w", cerr, err)
}
