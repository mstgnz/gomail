package gomail

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"sync"
)

// Variables for Pool configuration
var (
	defaultPoolSize = 10
)

// Pool structure
type Pool struct {
	connections chan *smtp.Client
	config      *Mail
	size        int
	mu          sync.Mutex
}

// NewPool creates a new connection pool
func NewPool(config *Mail, size int) (*Pool, error) {
	if size <= 0 {
		size = defaultPoolSize
	}

	pool := &Pool{
		connections: make(chan *smtp.Client, size),
		config:      config,
		size:        size,
	}

	// Initialize pool with connections
	for i := 0; i < size; i++ {
		client, err := pool.createConnection()
		if err != nil {
			return nil, fmt.Errorf("error initializing pool: %v", err)
		}
		pool.connections <- client
	}

	return pool, nil
}

// Create a new connection
func (p *Pool) createConnection() (*smtp.Client, error) {
	if p == nil || p.config == nil {
		return nil, fmt.Errorf("pool or config is not initialized")
	}

	addr := fmt.Sprintf("%s:%s", p.config.Host, p.config.Port)

	dialer := &net.Dialer{
		Timeout:   p.config.getTimeout(),
		KeepAlive: p.config.getKeepAlive(),
	}

	var conn net.Conn
	var err error

	if p.config.tlsConfig != nil && !p.config.tlsConfig.StartTLS {
		// Direct TLS connection
		tlsConfig := &tls.Config{
			InsecureSkipVerify: p.config.tlsConfig.InsecureSkipVerify,
			ServerName:         p.config.tlsConfig.ServerName,
			Certificates:       p.config.tlsConfig.Certificates,
		}
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	} else {
		// Plain connection for STARTTLS
		conn, err = dialer.Dial("tcp", addr)
	}

	if err != nil {
		return nil, err
	}

	client, err := smtp.NewClient(conn, p.config.Host)
	if err != nil {
		conn.Close()
		return nil, err
	}

	if p.config.tlsConfig != nil && p.config.tlsConfig.StartTLS {
		tlsConfig := &tls.Config{
			InsecureSkipVerify: p.config.tlsConfig.InsecureSkipVerify,
			ServerName:         p.config.tlsConfig.ServerName,
			Certificates:       p.config.tlsConfig.Certificates,
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			client.Close()
			return nil, fmt.Errorf("STARTTLS failed: %v", err)
		}
	}

	auth := smtp.PlainAuth("", p.config.User, p.config.Pass, p.config.Host)
	if err := client.Auth(auth); err != nil {
		client.Close()
		return nil, err
	}

	return client, nil
}

// Get a connection from the pool
func (p *Pool) getConnection() (*smtp.Client, error) {
	if p == nil || p.connections == nil {
		return nil, fmt.Errorf("pool is not initialized")
	}

	select {
	case client := <-p.connections:
		if client == nil {
			return p.createConnection()
		}
		return client, nil
	default:
		return p.createConnection()
	}
}

// Release a connection back to the pool
func (p *Pool) releaseConnection(client *smtp.Client) {
	if client == nil {
		return
	}

	select {
	case p.connections <- client:
	default:
		client.Close()
	}
}

// Close the pool and all its connections
func (p *Pool) Close() {
	if p == nil || p.connections == nil {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	close(p.connections)
	for client := range p.connections {
		if client != nil {
			client.Close()
		}
	}
}
