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
	// slots holds one token per checked-out connection; nil means no cap. New connections
	// are made only when no idle one is left, so open connections never exceed the cap.
	slots chan struct{}

	mu       sync.Mutex
	idle     []*poolConn
	closed   bool
	closeErr error // what checkout returns once closed
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
// size <= 0) and honours the Mail's SetMaxConnections. It opens no connection; the first
// send does.
func NewPool(config *Mail, size int) (*Pool, error) {
	if config == nil {
		return nil, errors.New("gomail: pool config is nil")
	}
	if size <= 0 {
		size = defaultPoolSize
	}
	p := &Pool{config: config, size: size}
	if config.maxConnections > 0 {
		p.slots = make(chan struct{}, config.maxConnections)
	}
	return p, nil
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
	if err := client.Auth(clientAuth{smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)}); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

// clientAuth marks errors the client raises before AUTH is sent (no TLS, wrong host): they
// are configuration errors, so a retry cannot fix them.
type clientAuth struct{ smtp.Auth }

func (a clientAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	proto, resp, err := a.Auth.Start(server)
	if err != nil {
		return "", nil, &authConfigError{err: err}
	}
	return proto, resp, nil
}

type authConfigError struct{ err error }

func (e *authConfigError) Error() string { return e.err.Error() }
func (e *authConfigError) Unwrap() error { return e.err }

// getConnection checks out a live connection, waiting for a slot when SetMaxConnections is
// set. Every connection it returns must go back through releaseConnection or discard.
func (p *Pool) getConnection(ctx context.Context) (*poolConn, error) {
	if p == nil || p.config == nil {
		return nil, errors.New("gomail: pool is not initialized")
	}
	if err := ctx.Err(); err != nil {
		return nil, err // before the NOOP probe, which a done ctx would fail on a healthy conn
	}
	if p.slots != nil {
		select {
		case p.slots <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		// select picks at random when the slot frees as ctx ends; do not probe with a done ctx.
		if err := ctx.Err(); err != nil {
			p.releaseSlot()
			return nil, err
		}
	}
	pc, err := p.checkout(ctx)
	if err != nil {
		p.releaseSlot()
		return nil, err
	}
	return pc, nil
}

func (p *Pool) releaseSlot() {
	if p.slots == nil {
		return
	}
	select {
	case <-p.slots:
	default: // more releases than checkouts would be a bug; never block on it
	}
}

// checkout returns the most recently used idle connection that answers NOOP, or a new one.
// Dead idle connections are closed and skipped, never returned as errors.
func (p *Pool) checkout(ctx context.Context) (*poolConn, error) {
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
		return nil, p.closeErr
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

// releaseConnection returns a healthy checked-out connection to the pool, or closes it when
// the pool is full or closed.
func (p *Pool) releaseConnection(pc *poolConn) {
	if pc == nil {
		return
	}
	defer p.releaseSlot()
	p.mu.Lock()
	if !p.closed && len(p.idle) < p.size {
		p.idle = append(p.idle, pc)
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	pc.close()
}

// discard closes a checked-out connection whose session state is unknown.
func (p *Pool) discard(pc *poolConn) {
	pc.close()
	p.releaseSlot()
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
		p.discard(pc)
		return
	}
	_ = pc.conn.SetDeadline(time.Now().Add(p.config.getTimeout()))
	if pc.client.Reset() != nil {
		p.discard(pc)
		return
	}
	_ = pc.conn.SetDeadline(time.Time{})
	p.releaseConnection(pc)
}

// Close closes all idle connections. Connections in use are closed when they are released.
func (p *Pool) Close() {
	p.closeWith(errPoolClosed)
}

// closeWith closes the pool; a send still waiting for a slot then gets err.
func (p *Pool) closeWith(err error) {
	if p == nil {
		return
	}
	p.mu.Lock()
	idle := p.idle
	p.idle = nil
	if !p.closed {
		p.closed, p.closeErr = true, err
	}
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
