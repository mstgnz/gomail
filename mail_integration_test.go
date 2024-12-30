package gomail

import (
	"bufio"
	"bytes"
	"errors"
	"log"
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

type mockSMTPServer struct {
	listener net.Listener
	messages []string
	quit     chan bool
	mu       sync.Mutex
}

func newMockSMTPServer(tb testingTB) *mockSMTPServer {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("Failed to create mock SMTP server: %v", err)
	}

	server := &mockSMTPServer{
		listener: listener,
		messages: make([]string, 0),
		quit:     make(chan bool),
	}

	go server.serve()
	return server
}

func (s *mockSMTPServer) serve() {
	for {
		select {
		case <-s.quit:
			return
		default:
			conn, err := s.listener.Accept()
			if err != nil {
				if !strings.Contains(err.Error(), "use of closed network connection") {
					log.Printf("Accept error: %v", err)
				}
				return
			}
			go s.handleConnection(conn)
		}
	}
}

func (s *mockSMTPServer) handleConnection(conn net.Conn) {
	defer func() {
		conn.Close()
		if r := recover(); r != nil {
			log.Printf("Recovered from panic in handleConnection: %v", r)
		}
	}()

	// Set connection timeouts
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Send greeting
	if _, err := conn.Write([]byte("220 mock.server ESMTP ready\r\n")); err != nil {
		return
	}

	reader := bufio.NewReader(conn)
	var message bytes.Buffer

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}

		message.WriteString(line)

		switch {
		case strings.HasPrefix(line, "EHLO"):
			conn.Write([]byte("250-mock.server\r\n250 AUTH PLAIN\r\n"))
		case strings.HasPrefix(line, "AUTH"):
			conn.Write([]byte("235 Authentication successful\r\n"))
		case strings.HasPrefix(line, "MAIL FROM"):
			conn.Write([]byte("250 Sender OK\r\n"))
		case strings.HasPrefix(line, "RCPT TO"):
			conn.Write([]byte("250 Recipient OK\r\n"))
		case strings.HasPrefix(line, "DATA"):
			conn.Write([]byte("354 Start mail input\r\n"))
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				message.WriteString(line)
				if line == ".\r\n" {
					break
				}
			}
			conn.Write([]byte("250 Message accepted\r\n"))
			s.mu.Lock()
			s.messages = append(s.messages, message.String())
			s.mu.Unlock()
			message.Reset()
		case strings.HasPrefix(line, "QUIT"):
			conn.Write([]byte("221 Bye\r\n"))
			return
		}
	}
}

func (s *mockSMTPServer) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.quit)
	s.listener.Close()
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

func TestMailIntegration(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()

	// Test kanalları
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

		// Mesajların işlenmesi için bekle
		time.Sleep(500 * time.Millisecond)

		messages := server.getMessages()
		if len(messages) == 0 {
			errChan <- errors.New("no messages received")
			return
		}

		msg := messages[0]
		if !strings.Contains(msg, "From: Test Sender <sender@example.com>") {
			errChan <- errors.New("message does not contain correct From header")
			return
		}
		if !strings.Contains(msg, "To: recipient@example.com") {
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

	// Test timeout kontrolü
	select {
	case <-testDone:
		return
	case err := <-errChan:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("test timed out after 10 seconds")
	}
}
