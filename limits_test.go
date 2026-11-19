package gomail

import (
	"context"
	"crypto/tls"
	"errors"
	"mime"
	"net/smtp"
	"strings"
	"testing"
	"time"
)

// sendWhenWaiting starts a send and returns once it waits for the rate limiter.
func sendWhenWaiting(t *testing.T, m *Mail) chan error {
	t.Helper()
	waiting := make(chan struct{}, 1)
	rateLimitWaitHook = func() {
		select {
		case waiting <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() { rateLimitWaitHook = nil })
	result := m.SendAsync()
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("the send never reached the rate limiter")
	}
	return result
}

func TestCloseWakesSendWaitingForRateLimiter(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	m.SetRateLimit(&RateLimit{Enabled: true, PerSecond: 1}) // first tick after 1s

	result := sendWhenWaiting(t, m)
	m.Close()

	select {
	case err := <-result:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("Send() error = %v, want ErrClosed", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Send is still blocked after Close")
	}
	if n := len(server.getData()); n != 0 {
		t.Errorf("server accepted %d messages after Close", n)
	}
	if !isPermanent(ErrClosed) {
		t.Error("ErrClosed should not be retried")
	}
}

func TestSetRateLimitReleasesWaitingSend(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	m.SetRateLimit(&RateLimit{Enabled: true, PerSecond: 1})

	result := sendWhenWaiting(t, m)
	m.SetRateLimit(nil) // turning the limit off lets the waiting send go at once

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Send() error = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Send is still blocked after the rate limit was turned off")
	}
}

func TestSetRateLimitMovesWaitingSendToNewLimit(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	m.SetRateLimit(&RateLimit{Enabled: true, PerSecond: 1})

	result := sendWhenWaiting(t, m)
	m.SetRateLimit(&RateLimit{Enabled: true, PerSecond: 20}) // first tick after 50ms

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Send() error = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("the waiting send did not move to the new, faster limit")
	}
}

func TestLongHeadersAreFolded(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	m.Subject = strings.TrimSpace(strings.Repeat("Booking confirmed for the evening match ", 12))
	m.Name = strings.TrimSpace(strings.Repeat("Field Booking Office ", 10))
	m.To = make([]string, 12)
	for i := range m.To {
		m.To[i] = "recipient" + strings.Repeat("x", i) + "@example.com"
	}

	if err := m.Send(); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	raw := onlyMessage(t, server)
	header := raw[:strings.Index(raw, "\r\n\r\n")]
	for line := range strings.SplitSeq(header, "\r\n") {
		if len(line) > 78 {
			t.Errorf("header line is %d octets, want at most 78: %q", len(line), line)
		}
		if strings.TrimSpace(line) == "" {
			t.Errorf("header has a whitespace-only line")
		}
	}
	msg := parseMessage(t, raw)
	if got := msg.Header.Get("Subject"); got != m.Subject {
		t.Errorf("unfolded Subject = %q, want %q", got, m.Subject)
	}
	from, err := msg.Header.AddressList("From")
	if err != nil || from[0].Name != m.Name {
		t.Errorf("From = %v (%v), want name %q", from, err, m.Name)
	}
	to, err := msg.Header.AddressList("To")
	if err != nil || len(to) != len(m.To) {
		t.Errorf("To has %d addresses (%v), want %d", len(to), err, len(m.To))
	}
}

func TestLongNonASCIISubjectIsSent(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	m.Subject = strings.Repeat("ç", 600) // RFC 2047 splits it into encoded words, which fold

	if err := m.Send(); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	msg := parseMessage(t, onlyMessage(t, server))
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || subject != m.Subject {
		t.Errorf("decoded Subject has %d runes (%v), want %d", len([]rune(subject)), err, 600)
	}
}

func TestUnfoldableHeaderIsRefused(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	m.Subject = strings.Repeat("a", 1000)

	if err := m.Send(); !errors.Is(err, ErrHeaderTooLong) || !strings.Contains(err.Error(), "Subject") {
		t.Fatalf("Send() error = %v, want ErrHeaderTooLong for Subject", err)
	}
	if _, err := m.PreviewEmail(); !errors.Is(err, ErrHeaderTooLong) {
		t.Errorf("PreviewEmail() error = %v, want ErrHeaderTooLong", err)
	}
	if n := server.connCount(); n != 0 {
		t.Errorf("connections = %d; a refused message must not reach the network", n)
	}
}

func TestFoldHeader(t *testing.T) {
	got, err := foldHeader("Subject", "a  b")
	if err != nil || got != "Subject: a  b\r\n" {
		t.Errorf("foldHeader() = %q, %v", got, err)
	}
	// A double space at a fold point must not leave a line of only whitespace.
	got, err = foldHeader("X", strings.Repeat("a", 77)+"  "+strings.Repeat("b", 77))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(strings.TrimSuffix(got, "\r\n"), "\r\n") {
		if strings.TrimSpace(line) == "" {
			t.Errorf("foldHeader() made a whitespace-only line: %q", got)
		}
	}
	// 997 octets fit on one line with "X: "; 998 would not.
	if _, err := foldHeader("X", strings.Repeat("a", 995)); err != nil {
		t.Errorf("foldHeader() at the limit error = %v", err)
	}
	if _, err := foldHeader("X", strings.Repeat("a", 996)); !errors.Is(err, ErrHeaderTooLong) {
		t.Errorf("foldHeader() over the limit error = %v, want ErrHeaderTooLong", err)
	}
}

func TestSendWithRetryStopsOnTLSVerificationError(t *testing.T) {
	cert, _ := testCertificate(t)
	server := newMockSMTPServerWith(t, mockOptions{tlsConfig: &tls.Config{Certificates: []tls.Certificate{cert}}, startTLS: true})
	defer server.close()
	m := newTestMail(t, server)
	m.SetTLSConfig(&TLSConfig{StartTLS: true})

	start := time.Now()
	err := m.SendWithRetry(3)
	if err == nil || !strings.Contains(err.Error(), "after 1 attempts") {
		t.Fatalf("SendWithRetry() error = %v, want one attempt", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("SendWithRetry() waited %v on a certificate error", elapsed)
	}
}

func TestClientAuthRefusalIsPermanent(t *testing.T) {
	auth := clientAuth{smtp.PlainAuth("", "user", "secret-pass", "smtp.example.com")}
	_, _, err := auth.Start(&smtp.ServerInfo{Name: "smtp.example.com", TLS: false})
	if err == nil || !isPermanent(err) {
		t.Fatalf("Start() over plain text error = %v, want a permanent error", err)
	}
	if strings.Contains(err.Error(), "secret-pass") {
		t.Errorf("error carries the password: %q", err)
	}
	if _, _, err := auth.Start(&smtp.ServerInfo{Name: "smtp.example.com", TLS: true, Auth: []string{"PLAIN"}}); err != nil {
		t.Errorf("Start() over TLS error = %v", err)
	}
}

func TestPoolReplacedWhenSettingsChangeInPlace(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)

	if err := m.Send(); err != nil {
		t.Fatal(err)
	}
	first := m.pool
	m.SetPoolSize(3)
	if err := m.Send(); err != nil {
		t.Fatal(err)
	}
	if m.pool == first {
		t.Error("SetPoolSize after the first send kept the old pool")
	}

	cfg := &TLSConfig{ServerName: "a.example.com"}
	before := newTLSKey(cfg)
	cfg.ServerName = "b.example.com"
	if newTLSKey(cfg) == before {
		t.Error("editing a TLSConfig in place does not change the pool key")
	}
	if newTLSKey(nil) == newTLSKey(&TLSConfig{}) {
		t.Error("no TLS and an empty TLSConfig share a pool key")
	}
}

func TestGetConnectionKeepsIdleConnectionWhenCtxDone(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	pool, _ := NewPool(m, 2)
	defer pool.Close()

	pc, err := pool.getConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pool.releaseConnection(pc)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pool.getConnection(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("getConnection() error = %v, want Canceled", err)
	}
	pc, err = pool.getConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pool.releaseConnection(pc)
	if n := server.connCount(); n != 1 {
		t.Errorf("connections = %d, want 1: the idle one must survive a cancelled ctx", n)
	}
}

func TestMaxConnectionsCapsOpenConnections(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	m.SetMaxConnections(2)

	results := make([]chan error, 12)
	for i := range results {
		results[i] = m.SendAsync()
	}
	for i, r := range results {
		if err := <-r; err != nil {
			t.Errorf("SendAsync #%d error = %v", i, err)
		}
	}
	if n := len(server.getData()); n != 12 {
		t.Errorf("server accepted %d messages, want 12", n)
	}
	if peak := server.peakConns(); peak > 2 {
		t.Errorf("peak open connections = %d, want at most 2", peak)
	}
}

func TestMaxConnectionsWaitHonoursContext(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	m.SetMaxConnections(1)

	pool, err := m.getPool()
	if err != nil {
		t.Fatal(err)
	}
	held, err := pool.getConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := m.SendContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendContext() error = %v, want DeadlineExceeded while the only slot is taken", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("SendContext() waited %v", elapsed)
	}

	pool.releaseConnection(held)
	if err := m.Send(); err != nil {
		t.Fatalf("Send() after the slot was freed error = %v", err)
	}
}

func TestSendAfterCloseWorks(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	m.SetRateLimit(&RateLimit{Enabled: true, PerSecond: 20})

	if err := m.Send(); err != nil {
		t.Fatal(err)
	}
	m.Close()
	if err := m.Send(); err != nil {
		t.Fatalf("Send() after Close error = %v", err)
	}
	if n := len(server.getData()); n != 2 {
		t.Errorf("server accepted %d messages, want 2", n)
	}
}

func TestTLSAgainstPlainServerIsPermanent(t *testing.T) {
	server := newMockSMTPServer(t) // plain text, so a TLS handshake reads "220 ..." as a record
	defer server.close()
	m := newTestMail(t, server)
	m.SetTLSConfig(&TLSConfig{StartTLS: false})

	start := time.Now()
	err := m.SendWithRetry(3)
	if err == nil || !strings.Contains(err.Error(), "after 1 attempts") {
		t.Fatalf("SendWithRetry() error = %v, want one attempt", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("SendWithRetry() waited %v on a TLS/plain mismatch", elapsed)
	}
}

// holdOnlySlot sets a cap of 1 and checks out the only connection.
func holdOnlySlot(t *testing.T, m *Mail) (*Pool, *poolConn) {
	t.Helper()
	m.SetMaxConnections(1)
	pool, err := m.getPool()
	if err != nil {
		t.Fatal(err)
	}
	held, err := pool.getConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return pool, held
}

func TestCloseWakesSendWaitingForSlot(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	pool, held := holdOnlySlot(t, m)

	result := m.SendAsync()
	time.Sleep(100 * time.Millisecond) // the send waits for the slot; if not yet, it fails the same way
	m.Close()
	pool.releaseConnection(held) // the slot frees; the waiter finds the pool closed

	select {
	case err := <-result:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("Send() error = %v, want ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send is still blocked")
	}
	if n := len(server.getData()); n != 0 {
		t.Errorf("server accepted %d messages after Close", n)
	}
}

func TestSendWaitingForSlotMovesToReplacedPool(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	pool, held := holdOnlySlot(t, m)

	result := m.SendAsync()
	time.Sleep(100 * time.Millisecond)
	// Replace the pool as getPool does on a settings change. A setter here would race with
	// the running send, which the Mail contract forbids.
	m.mu.Lock()
	m.pool.Close()
	m.pool = nil
	m.mu.Unlock()
	pool.releaseConnection(held)

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Send() error = %v; it should continue on the new pool", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send is still blocked")
	}
}

func TestConnectionCapSurvivesPoolReplacement(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	pool, held := holdOnlySlot(t, m)
	defer pool.releaseConnection(held)

	m.SetPoolSize(3) // replaces the pool; the held connection must still count
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := m.SendContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendContext() error = %v; a second connection opened past the cap of 1", err)
	}
	if peak := server.peakConns(); peak > 1 {
		t.Errorf("peak open connections = %d, want 1", peak)
	}
}

func TestOverlongPartHeadersAreRefused(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()

	longTurkish := strings.Repeat("ş", 200) + ".pdf" // RFC 2231: 6 octets per rune
	cases := map[string]func(*Mail){
		"map filename": func(m *Mail) { m.SetAttachment(map[string][]byte{longTurkish: []byte("x")}) },
		"stream filename": func(m *Mail) {
			m.SetStreamAttachment([]AttachmentReader{{Name: longTurkish, Reader: strings.NewReader("x")}})
		},
		"typed filename": func(m *Mail) { m.SetAttachments([]Attachment{{Name: longTurkish}}) },
		"inline name":    func(m *Mail) { m.SetAttachments([]Attachment{{Name: strings.Repeat("a", 1000), Inline: true}}) },
		"content type": func(m *Mail) {
			m.SetAttachments([]Attachment{{Name: "a.bin", ContentType: "application/x-a; p=" + strings.Repeat("b", 1000)}})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := newTestMail(t, server)
			mutate(m)
			if err := m.Send(); !errors.Is(err, ErrHeaderTooLong) {
				t.Errorf("Send() error = %v, want ErrHeaderTooLong", err)
			}
		})
	}
	if n := server.connCount(); n != 0 {
		t.Errorf("connections = %d; a refused message must not reach the network", n)
	}

	// 100 Turkish runes fit: the limit refuses only what cannot be written.
	m := newTestMail(t, server)
	m.SetAttachment(map[string][]byte{strings.Repeat("ş", 100) + ".pdf": []byte("x")})
	if err := m.Send(); err != nil {
		t.Errorf("Send() with a 100-rune filename error = %v", err)
	}
}

func TestDiscardFreesSlot(t *testing.T) {
	server := newMockSMTPServerWith(t, mockOptions{dataReply: "554 rejected"})
	defer server.close()
	m := newTestMail(t, server)
	m.SetMaxConnections(1)

	// A body failure discards the connection; the slot must come back or the next send hangs.
	m.SetStreamAttachment([]AttachmentReader{{Name: "a.txt", Reader: failingReader{errors.New("boom")}}})
	_ = m.Send()
	m.SetStreamAttachment(nil)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.SendContext(ctx); errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("the slot of a discarded connection was not freed")
	}
}
