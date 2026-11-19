package gomail

import (
	"bufio"
	"bytes"
	"cmp"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// testingTB interface for testing
type testingTB interface {
	Fatalf(format string, args ...any)
}

// mockOptions changes how the fake server answers. They are fixed before it accepts.
type mockOptions struct {
	dataReply        string      // reply to the DATA terminator; empty means 250
	dropAfterMessage bool        // close the connection after answering the terminator
	stallData        bool        // read the terminator but never answer it
	stallGreeting    bool        // accept the connection but never greet
	tempFailures     int         // answer 451 to the first N DATA terminators
	rcptReply        string      // reply to RCPT TO; empty means 250
	authReply        string      // reply to AUTH; empty means 235
	tlsConfig        *tls.Config // with startTLS, offered via STARTTLS; otherwise implicit TLS
	startTLS         bool
}

// mockSMTPServer is an in-process SMTP server that records what clients send.
type mockSMTPServer struct {
	listener net.Listener
	opts     mockOptions
	quit     chan bool

	mu       sync.Mutex
	messages []string // session transcript per accepted message
	data     []string // raw message (headers and body) per accepted message
	commands []string // every command line, across connections
	conns    int
	failed   int // 451 replies given so far
}

func newMockSMTPServer(tb testingTB) *mockSMTPServer {
	return newMockSMTPServerWith(tb, mockOptions{})
}

func newMockSMTPServerWith(tb testingTB, opts mockOptions) *mockSMTPServer {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("Failed to create mock SMTP server: %v", err)
	}
	if opts.tlsConfig != nil && !opts.startTLS {
		listener = tls.NewListener(listener, opts.tlsConfig)
	}

	server := &mockSMTPServer{
		listener: listener,
		opts:     opts,
		quit:     make(chan bool),
	}

	go server.serve()
	return server
}

func (s *mockSMTPServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConnection(conn)
	}
}

func (s *mockSMTPServer) handleConnection(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	s.mu.Lock()
	s.conns++
	s.mu.Unlock()

	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(conn)

	if s.opts.stallGreeting {
		_, _ = io.Copy(io.Discard, reader)
		return
	}

	reply := func(lines string) bool {
		_, err := conn.Write([]byte(lines + "\r\n"))
		return err == nil
	}
	if !reply("220 mock.server ESMTP ready") {
		return
	}

	var transcript bytes.Buffer
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		transcript.WriteString(line)
		s.mu.Lock()
		s.commands = append(s.commands, strings.TrimRight(line, "\r\n"))
		s.mu.Unlock()

		switch cmd := strings.ToUpper(line); {
		case strings.HasPrefix(cmd, "EHLO"):
			if s.opts.startTLS {
				reply("250-mock.server\r\n250-STARTTLS\r\n250 AUTH PLAIN")
			} else {
				reply("250-mock.server\r\n250 AUTH PLAIN")
			}
		case strings.HasPrefix(cmd, "HELO"):
			reply("250 mock.server")
		case strings.HasPrefix(cmd, "STARTTLS"):
			reply("220 Ready to start TLS")
			tlsConn := tls.Server(conn, s.opts.tlsConfig)
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			conn = tlsConn
			reader = bufio.NewReader(conn)
		case strings.HasPrefix(cmd, "AUTH"):
			reply(cmp.Or(s.opts.authReply, "235 Authentication successful"))
		case strings.HasPrefix(cmd, "MAIL FROM"):
			reply("250 Sender OK")
		case strings.HasPrefix(cmd, "RCPT TO"):
			reply(cmp.Or(s.opts.rcptReply, "250 Recipient OK"))
		case strings.HasPrefix(cmd, "NOOP"), strings.HasPrefix(cmd, "RSET"):
			reply("250 OK")
		case strings.HasPrefix(cmd, "DATA"):
			reply("354 Start mail input")
			var body bytes.Buffer
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return // connection dropped mid-message: nothing is accepted
				}
				transcript.WriteString(line)
				if line == ".\r\n" {
					break
				}
				body.WriteString(line)
			}
			if s.opts.stallData {
				_, _ = io.Copy(io.Discard, reader)
				return
			}
			answer := s.opts.dataReply
			if answer == "" {
				answer = "250 Message accepted"
			}
			s.mu.Lock()
			if s.failed < s.opts.tempFailures {
				s.failed++
				answer = "451 4.3.0 Try again later"
			}
			s.mu.Unlock()
			// Record before replying, so a test reading after Send returns sees the message.
			if strings.HasPrefix(answer, "250") {
				s.mu.Lock()
				s.messages = append(s.messages, transcript.String())
				s.data = append(s.data, body.String())
				s.mu.Unlock()
			}
			reply(answer)
			transcript.Reset()
			if s.opts.dropAfterMessage {
				return
			}
		case strings.HasPrefix(cmd, "QUIT"):
			reply("221 Bye")
			return
		default:
			reply("500 Unrecognized command")
		}
	}
}

func (s *mockSMTPServer) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.quit)
	_ = s.listener.Close()
	s.messages = nil
}

func (s *mockSMTPServer) addr() string {
	return s.listener.Addr().String()
}

func (s *mockSMTPServer) getMessages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.messages...)
}

func (s *mockSMTPServer) getData() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.data...)
}

func (s *mockSMTPServer) getCommands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.commands...)
}

func (s *mockSMTPServer) connCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns
}

func TestMailIntegration(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()

	testDone := make(chan bool)
	errChan := make(chan error)

	go func() {
		defer close(testDone)
		defer close(errChan)

		host, port, _ := net.SplitHostPort(server.addr())

		mail := &Mail{
			From:      "sender@example.com",
			Name:      "Test Sender",
			Host:      host,
			Port:      port,
			User:      "user",
			Pass:      "pass",
			Subject:   "Integration Test",
			Content:   "Test Content",
			To:        []string{"recipient@example.com"},
			Timeout:   5 * time.Second,
			KeepAlive: 10 * time.Second,
		}

		if err := mail.Send(); err != nil {
			errChan <- err
			return
		}

		// Send returns after the server's 250, which is recorded before it is written.
		messages := server.getMessages()
		if len(messages) == 0 {
			errChan <- errors.New("no messages received")
			return
		}

		msg := messages[0]
		if !strings.Contains(msg, `From: "Test Sender" <sender@example.com>`) {
			errChan <- errors.New("message does not contain correct From header")
			return
		}
		if !strings.Contains(msg, "To: <recipient@example.com>") {
			errChan <- errors.New("message does not contain correct To header")
			return
		}
		if !strings.Contains(msg, "Subject: Integration Test") {
			errChan <- errors.New("message does not contain correct Subject header")
			return
		}
		if !strings.Contains(msg, "Test Content") {
			errChan <- errors.New("message does not contain correct content")
			return
		}

		testDone <- true
	}()

	select {
	case <-testDone:
		return
	case err := <-errChan:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("test timed out after 10 seconds")
	}
}
