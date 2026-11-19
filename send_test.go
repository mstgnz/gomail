package gomail

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"math/big"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestMail(t *testing.T, server *mockSMTPServer) *Mail {
	t.Helper()
	host, port, err := net.SplitHostPort(server.addr())
	if err != nil {
		t.Fatal(err)
	}
	m := &Mail{
		From:    "sender@example.com",
		Name:    "Test Sender",
		Host:    host,
		Port:    port,
		User:    "user",
		Pass:    "pass",
		Subject: "Test Subject",
		Content: "<p>Test Content</p>",
		To:      []string{"recipient@example.com"},
	}
	t.Cleanup(m.Close)
	return m
}

func onlyMessage(t *testing.T, server *mockSMTPServer) string {
	t.Helper()
	data := server.getData()
	if len(data) != 1 {
		t.Fatalf("server accepted %d messages, want 1", len(data))
	}
	return data[0]
}

func parseMessage(t *testing.T, raw string) *mail.Message {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	return msg
}

type mimePart struct {
	contentType string
	encoding    string
	disposition string
	body        []byte
}

// readParts flattens a multipart body into its leaf parts, decoding each transfer encoding.
func readParts(t *testing.T, contentType string, r io.Reader) []mimePart {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		t.Fatalf("not a multipart content type %q: %v", contentType, err)
	}
	mr := multipart.NewReader(r, params["boundary"])
	var parts []mimePart
	for {
		p, err := mr.NextRawPart()
		if err == io.EOF {
			return parts
		}
		if err != nil {
			t.Fatalf("NextRawPart: %v", err)
		}
		ct := p.Header.Get("Content-Type")
		if strings.HasPrefix(ct, "multipart/") {
			parts = append(parts, readParts(t, ct, p)...)
			continue
		}
		cte := p.Header.Get("Content-Transfer-Encoding")
		var body io.Reader = p
		switch cte {
		case "base64":
			body = base64.NewDecoder(base64.StdEncoding, p)
		case "quoted-printable":
			body = quotedprintable.NewReader(p)
		}
		data, err := io.ReadAll(body)
		if err != nil {
			t.Fatalf("reading part %q: %v", ct, err)
		}
		parts = append(parts, mimePart{contentType: ct, encoding: cte, disposition: p.Header.Get("Content-Disposition"), body: data})
	}
}

func messageParts(t *testing.T, raw string) []mimePart {
	t.Helper()
	msg := parseMessage(t, raw)
	return readParts(t, msg.Header.Get("Content-Type"), msg.Body)
}

func TestSendReportsRejectedMessage(t *testing.T) {
	server := newMockSMTPServerWith(t, mockOptions{dataReply: "554 5.7.1 Message rejected"})
	defer server.close()
	m := newTestMail(t, server)

	err := m.Send()
	var reply *textproto.Error
	if !errors.As(err, &reply) || reply.Code != 554 {
		t.Fatalf("Send() error = %v, want a 554 reply", err)
	}

	// A reply error leaves the session in sync: RSET, then the connection is reused.
	if err := m.Send(); !errors.As(err, &reply) || reply.Code != 554 {
		t.Fatalf("second Send() error = %v, want a 554 reply", err)
	}
	if !slices.Contains(server.getCommands(), "RSET") {
		t.Error("no RSET after a rejected message")
	}
	if n := server.connCount(); n != 1 {
		t.Errorf("connections = %d, want 1 (reused after RSET)", n)
	}
}

func TestSendReportsRejectedRecipient(t *testing.T) {
	server := newMockSMTPServerWith(t, mockOptions{rcptReply: "550 5.1.1 Mailbox unavailable"})
	defer server.close()
	m := newTestMail(t, server)

	var reply *textproto.Error
	for range 2 {
		if err := m.Send(); !errors.As(err, &reply) || reply.Code != 550 {
			t.Fatalf("Send() error = %v, want a 550 reply", err)
		}
	}
	if n := server.connCount(); n != 1 {
		t.Errorf("connections = %d, want 1 (reused after RSET)", n)
	}
	if slices.Contains(server.getCommands(), "DATA") {
		t.Error("DATA was sent after the recipient was refused")
	}
}

func TestSendReportsAuthFailure(t *testing.T) {
	server := newMockSMTPServerWith(t, mockOptions{authReply: "535 5.7.8 Authentication failed"})
	defer server.close()
	m := newTestMail(t, server)

	err := m.Send()
	var reply *textproto.Error
	if !errors.As(err, &reply) || reply.Code != 535 {
		t.Fatalf("Send() error = %v, want a 535 reply", err)
	}
	if strings.Contains(err.Error(), m.Pass) {
		t.Errorf("error carries the password: %q", err)
	}
	if !isPermanent(err) {
		t.Error("an authentication failure should not be retried")
	}
}

func TestSendReplacesDeadPooledConnection(t *testing.T) {
	server := newMockSMTPServerWith(t, mockOptions{dropAfterMessage: true})
	defer server.close()
	m := newTestMail(t, server)
	m.SetPoolSize(1)

	for i := range 3 {
		if err := m.Send(); err != nil {
			t.Fatalf("Send #%d error = %v; a dead pooled connection must be replaced", i+1, err)
		}
	}
	if n := len(server.getData()); n != 3 {
		t.Errorf("server accepted %d messages, want 3", n)
	}
	if n := server.connCount(); n != 3 {
		t.Errorf("connections = %d, want 3", n)
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestSendDropsConnectionWhenBodyFails(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)

	errDisk := errors.New("disk read failed")
	m.SetStreamAttachment([]AttachmentReader{{
		Name:   "invoice.pdf",
		Reader: io.MultiReader(strings.NewReader("partial content"), failingReader{errDisk}),
	}})
	if err := m.Send(); !errors.Is(err, errDisk) {
		t.Fatalf("Send() error = %v, want %v", err, errDisk)
	}
	// The terminator must not be sent, or the server would deliver a truncated message.
	if n := len(server.getData()); n != 0 {
		t.Fatalf("server accepted %d truncated messages", n)
	}

	m.SetStreamAttachment(nil)
	if err := m.Send(); err != nil {
		t.Fatalf("Send() after a failed body error = %v", err)
	}
	if n := server.connCount(); n != 2 {
		t.Errorf("connections = %d, want 2 (the broken one is not reused)", n)
	}
}

func TestSendEncodesTurkishText(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	m.Subject = "Rezervasyonun onaylandı: ğüşıöçİ ĞÜŞIÖÇ, saat 19:00 sahası hazır"
	m.Name = "Saha Rezervasyon Şubesi"
	m.Content = "<p>Merhaba Ayşe, rezervasyonun onaylandı. Görüşmek üzere!</p>"
	m.SetAttachment(map[string][]byte{"özet çıktı.pdf": []byte("%PDF-1.4 test")})

	if err := m.Send(); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	raw := onlyMessage(t, server)

	header := raw[:strings.Index(raw, "\r\n\r\n")]
	for i := 0; i < len(header); i++ {
		if header[i] > 0x7e {
			t.Fatalf("header has a raw non-ASCII byte at %d: %q", i, header)
		}
	}

	msg := parseMessage(t, raw)
	rawSubject := msg.Header.Get("Subject")
	if !strings.HasPrefix(rawSubject, "=?utf-8?q?") {
		t.Errorf("Subject is not RFC 2047 encoded: %q", rawSubject)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(rawSubject)
	if err != nil || subject != m.Subject {
		t.Errorf("decoded Subject = %q (%v), want %q", subject, err, m.Subject)
	}
	from, err := msg.Header.AddressList("From")
	if err != nil || len(from) != 1 || from[0].Name != m.Name || from[0].Address != m.From {
		t.Errorf("From = %v (%v), want %q <%s>", from, err, m.Name, m.From)
	}

	parts := messageParts(t, raw)
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want body and attachment", len(parts))
	}
	if parts[0].encoding != "quoted-printable" || !strings.HasPrefix(parts[0].contentType, "text/html") {
		t.Errorf("body part = %q/%q, want quoted-printable text/html", parts[0].contentType, parts[0].encoding)
	}
	if string(parts[0].body) != m.Content {
		t.Errorf("body = %q, want %q", parts[0].body, m.Content)
	}
	_, params, err := mime.ParseMediaType(parts[1].disposition)
	if err != nil || params["filename"] != "özet çıktı.pdf" {
		t.Errorf("attachment filename = %q (%v)", params["filename"], err)
	}
	if string(parts[1].body) != "%PDF-1.4 test" {
		t.Errorf("attachment body = %q", parts[1].body)
	}
}

func TestSendPlainTextAlternative(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	m.SetPlainText("Rezervasyonun onaylandı.")

	if err := m.Send(); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	raw := onlyMessage(t, server)
	if !strings.Contains(raw, "multipart/alternative") {
		t.Error("no multipart/alternative part")
	}
	parts := messageParts(t, raw)
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want text and html", len(parts))
	}
	if !strings.HasPrefix(parts[0].contentType, "text/plain") || string(parts[0].body) != m.PlainText {
		t.Errorf("first alternative = %q %q, want the plain text", parts[0].contentType, parts[0].body)
	}
	if !strings.HasPrefix(parts[1].contentType, "text/html") || string(parts[1].body) != m.Content {
		t.Errorf("second alternative = %q %q, want the HTML", parts[1].contentType, parts[1].body)
	}
}

func TestSendHonoursContentType(t *testing.T) {
	for _, ct := range []ContentType{TextPlain, TextMarkdown} {
		t.Run(string(ct), func(t *testing.T) {
			server := newMockSMTPServer(t)
			defer server.close()
			m := newTestMail(t, server)
			m.SetContentType(ct).SetContent("Line one\nLine two").SetPlainText("ignored")

			if err := m.Send(); err != nil {
				t.Fatalf("Send() error = %v", err)
			}
			parts := messageParts(t, onlyMessage(t, server))
			if len(parts) != 1 || !strings.HasPrefix(parts[0].contentType, "text/plain") {
				t.Fatalf("parts = %+v, want one text/plain part", parts)
			}
			if got := string(parts[0].body); got != "Line one\r\nLine two" {
				t.Errorf("body = %q", got)
			}
		})
	}
}

func TestSendKeepsLinesShort(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	m.Content = "<p>" + strings.Repeat("ç", 3000) + strings.Repeat("a", 3000) + "</p>"
	attachment := make([]byte, 10_000)
	_, _ = rand.Read(attachment)
	m.SetAttachment(map[string][]byte{"data.bin": attachment})
	m.To = []string{
		"a-long-recipient-address-0@example.com", "a-long-recipient-address-1@example.com",
		"a-long-recipient-address-2@example.com", "a-long-recipient-address-3@example.com",
	}

	if err := m.Send(); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	raw := onlyMessage(t, server)
	for i, line := range strings.Split(raw, "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line %d is %d octets; RFC 5322 allows 998", i, len(line))
		}
	}
	parts := messageParts(t, raw)
	if string(parts[0].body) != m.Content || !bytes.Equal(parts[1].body, attachment) {
		t.Error("content changed in transfer")
	}
}

func TestSendOmitsBccAndEmptyCc(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	m.Bcc = []string{"hidden@example.com"}

	if err := m.Send(); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	raw := onlyMessage(t, server)
	msg := parseMessage(t, raw)
	if _, ok := msg.Header["Bcc"]; ok {
		t.Error("message has a Bcc header")
	}
	if _, ok := msg.Header["Cc"]; ok {
		t.Error("message has an empty Cc header")
	}
	if strings.Contains(raw, "hidden@example.com") {
		t.Error("the blind recipient appears in the message")
	}
	if !slices.Contains(server.getCommands(), "RCPT TO:<hidden@example.com>") {
		t.Error("the blind recipient was not given to RCPT TO")
	}
}

var messageIDPattern = regexp.MustCompile(`^<[A-Z2-7]+@example\.com>$`)

func TestSendWritesDateAndMessageID(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)

	for range 2 {
		if err := m.Send(); err != nil {
			t.Fatalf("Send() error = %v", err)
		}
	}
	data := server.getData()
	ids := map[string]bool{}
	for _, raw := range data {
		msg := parseMessage(t, raw)
		date, err := msg.Header.Date()
		if err != nil || time.Since(date) > time.Minute {
			t.Errorf("Date = %v (%v)", date, err)
		}
		id := msg.Header.Get("Message-ID")
		if !messageIDPattern.MatchString(id) {
			t.Errorf("Message-ID = %q", id)
		}
		ids[id] = true
	}
	if len(ids) != 2 {
		t.Errorf("Message-IDs are not unique: %v", ids)
	}
}

func TestSendRefusesLineBreaksInHeaders(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()

	const inject = "\r\nBcc: victim@example.com"
	cases := []struct {
		name   string
		mutate func(*Mail)
	}{
		{"Subject CRLF", func(m *Mail) { m.Subject += inject }},
		{"Subject LF", func(m *Mail) { m.Subject += "\nX-Evil: 1" }},
		{"Subject CR", func(m *Mail) { m.Subject += "\rX-Evil: 1" }},
		{"Name", func(m *Mail) { m.Name += inject }},
		{"From", func(m *Mail) { m.From += inject }},
		{"To", func(m *Mail) { m.To = []string{"recipient@example.com" + inject} }},
		{"Cc", func(m *Mail) { m.Cc = []string{"cc@example.com" + inject} }},
		{"Bcc", func(m *Mail) { m.Bcc = []string{"bcc@example.com" + inject} }},
		{"attachment filename", func(m *Mail) { m.Attachments = map[string][]byte{"a.pdf" + inject: []byte("x")} }},
		{"stream attachment filename", func(m *Mail) {
			m.SetStreamAttachment([]AttachmentReader{{Name: "a.pdf\nX-Evil: 1", Reader: strings.NewReader("x")}})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestMail(t, server)
			tc.mutate(m)
			if err := m.Send(); !errors.Is(err, ErrInvalidHeader) {
				t.Errorf("Send() error = %v, want ErrInvalidHeader", err)
			}
			if _, err := m.PreviewEmail(); !errors.Is(err, ErrInvalidHeader) {
				t.Errorf("PreviewEmail() error = %v, want ErrInvalidHeader", err)
			}
		})
	}
	if n := server.connCount(); n != 0 {
		t.Errorf("connections = %d; a refused message must not reach the network", n)
	}
}

func TestValidationErrorsDoNotCarryValues(t *testing.T) {
	m := &Mail{
		From: "sender@example.com", Name: "Sender", Host: "127.0.0.1", Port: "1",
		User: "user", Pass: "pass", Subject: "Subject", Content: "Content",
		To:  []string{"recipient@example.com"},
		Bcc: []string{"ok@example.com", "secret.value-no-at-sign"},
	}
	err := m.Send()
	if !errors.Is(err, ErrInvalidAddress) {
		t.Fatalf("Send() error = %v, want ErrInvalidAddress", err)
	}
	if strings.Contains(err.Error(), "secret.value") {
		t.Errorf("error carries the address: %q", err)
	}
	if !strings.Contains(err.Error(), "Bcc[1]") {
		t.Errorf("error does not name the field: %q", err)
	}

	m.Subject = ""
	if err := m.Send(); !errors.Is(err, ErrMissingParameter) || err.Error() != "missing parameter" {
		t.Errorf("Send() error = %v, want ErrMissingParameter", err)
	}

	m.Subject = "Subject"
	m.Bcc = nil
	m.SetStreamAttachment([]AttachmentReader{{Name: "a.txt"}})
	if err := m.Send(); !errors.Is(err, ErrMissingParameter) {
		t.Errorf("Send() with a nil stream reader error = %v, want ErrMissingParameter", err)
	}
}

func TestLibraryWritesNothingToLog(t *testing.T) {
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	invalid := &Mail{
		From: "sender@example.com", Name: "Sender", Host: "127.0.0.1", Port: "1",
		User: "user", Pass: "pass", Subject: "Subject", Content: "Content",
		To: []string{"customer-private@example"}, Cc: []string{"bad"}, Bcc: []string{"bad"},
	}
	_ = invalid.Send()

	server := newMockSMTPServerWith(t, mockOptions{tempFailures: 1})
	defer server.close()
	m := newTestMail(t, server)
	if err := m.SendWithRetry(2); err != nil { // fails once, retries, succeeds
		t.Fatalf("SendWithRetry() error = %v", err)
	}

	if buf.Len() != 0 {
		t.Errorf("the library wrote to the standard logger: %q", buf.String())
	}
}

func TestSendWithRetryStopsOnPermanentError(t *testing.T) {
	server := newMockSMTPServerWith(t, mockOptions{dataReply: "554 5.7.1 Message rejected"})
	defer server.close()
	m := newTestMail(t, server)

	start := time.Now()
	err := m.SendWithRetry(3)
	var reply *textproto.Error
	if !errors.As(err, &reply) || reply.Code != 554 {
		t.Fatalf("SendWithRetry() error = %v, want a wrapped 554 reply", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("SendWithRetry() waited %v on a permanent error", elapsed)
	}
	if !strings.Contains(err.Error(), "after 1 attempts") {
		t.Errorf("error = %q, want one attempt", err)
	}

	m.To = []string{"bad"}
	if err := m.SendWithRetry(3); !errors.Is(err, ErrInvalidAddress) {
		t.Errorf("SendWithRetry() error = %v, want ErrInvalidAddress", err)
	}
	if err := m.SendWithRetry(0); err == nil {
		t.Error("SendWithRetry(0) should fail")
	}
}

func TestSendWithRetryRewindsStreams(t *testing.T) {
	server := newMockSMTPServerWith(t, mockOptions{tempFailures: 1})
	defer server.close()
	m := newTestMail(t, server)
	const body = "attachment body that must arrive whole"
	m.SetStreamAttachment([]AttachmentReader{{Name: "a.txt", Reader: strings.NewReader(body)}})

	if err := m.SendWithRetry(2); err != nil {
		t.Fatalf("SendWithRetry() error = %v", err)
	}
	parts := messageParts(t, onlyMessage(t, server))
	if got := string(parts[len(parts)-1].body); got != body {
		t.Errorf("attachment after retry = %q, want %q", got, body)
	}
}

func TestSendWithRetrySkipsRetryForUnseekableStream(t *testing.T) {
	server := newMockSMTPServerWith(t, mockOptions{tempFailures: 1})
	defer server.close()
	m := newTestMail(t, server)
	m.SetStreamAttachment([]AttachmentReader{{Name: "a.txt", Reader: io.MultiReader(strings.NewReader("once"))}})

	err := m.SendWithRetry(3)
	var reply *textproto.Error
	if !errors.As(err, &reply) || reply.Code != 451 || !strings.Contains(err.Error(), "after 1 attempts") {
		t.Fatalf("SendWithRetry() error = %v, want one 451 attempt", err)
	}
	if n := len(server.getData()); n != 0 {
		t.Errorf("server accepted %d messages with a consumed attachment", n)
	}
}

func TestSendWithRetryContextCancelledWhileWaiting(t *testing.T) {
	server := newMockSMTPServerWith(t, mockOptions{tempFailures: 5})
	defer server.close()
	m := newTestMail(t, server)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := m.SendWithRetryContext(ctx, 3)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendWithRetryContext() error = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("SendWithRetryContext() took %v; the backoff must honour ctx", elapsed)
	}
}

func TestSendContextDeadline(t *testing.T) {
	server := newMockSMTPServerWith(t, mockOptions{stallData: true})
	defer server.close()
	m := newTestMail(t, server)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := m.SendContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendContext() error = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("SendContext() returned after %v", elapsed)
	}
}

func TestSendContextCancel(t *testing.T) {
	server := newMockSMTPServerWith(t, mockOptions{stallData: true})
	defer server.close()
	m := newTestMail(t, server)

	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(300*time.Millisecond, cancel)
	defer timer.Stop()
	if err := m.SendContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("SendContext() error = %v, want Canceled", err)
	}
}

func TestSendContextAlreadyExpired(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := m.SendContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendContext() error = %v, want DeadlineExceeded", err)
	}
	if n := server.connCount(); n != 0 {
		t.Errorf("connections = %d, want 0", n)
	}
}

func TestSendContextWhileRateLimited(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	m.SetRateLimit(&RateLimit{Enabled: true, PerSecond: 1})

	// The first tick comes after 1s; ctx must end the wait before that.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := m.SendContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendContext() error = %v, want DeadlineExceeded while waiting for the limiter", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("SendContext() waited %v for the limiter after ctx expired", elapsed)
	}
}

func TestTimeoutBoundsHandshake(t *testing.T) {
	server := newMockSMTPServerWith(t, mockOptions{stallGreeting: true})
	defer server.close()
	m := newTestMail(t, server)
	m.SetTimeout(300 * time.Millisecond)

	start := time.Now()
	if err := m.Send(); err == nil {
		t.Fatal("Send() to a server that never greets succeeded")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Send() returned after %v; Timeout must bound the handshake", elapsed)
	}
}

func TestNewPoolIsLazy(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)

	pool, err := NewPool(m, 5)
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer pool.Close()
	if n := server.connCount(); n != 0 {
		t.Fatalf("NewPool opened %d connections, want 0", n)
	}
	pc, err := pool.getConnection(context.Background())
	if err != nil {
		t.Fatalf("getConnection() error = %v", err)
	}
	pool.releaseConnection(pc)
	if n := server.connCount(); n != 1 {
		t.Errorf("connections = %d, want 1", n)
	}

	if _, err := NewPool(nil, 1); err == nil {
		t.Error("NewPool(nil) should fail")
	}
}

func TestPoolCloseWhileReleasing(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)

	for range 20 {
		pool, _ := NewPool(m, 2)
		conns := make([]*poolConn, 4)
		for i := range conns {
			pc, err := pool.getConnection(context.Background())
			if err != nil {
				t.Fatalf("getConnection() error = %v", err)
			}
			conns[i] = pc
		}
		var wg sync.WaitGroup
		for _, pc := range conns {
			wg.Go(func() { pool.releaseConnection(pc) })
		}
		wg.Go(pool.Close)
		wg.Wait()

		if _, err := pool.getConnection(context.Background()); !errors.Is(err, errPoolClosed) {
			t.Fatalf("getConnection() after Close error = %v, want errPoolClosed", err)
		}
		pool.mu.Lock()
		idle := len(pool.idle)
		pool.mu.Unlock()
		if idle != 0 {
			t.Fatalf("closed pool keeps %d idle connections", idle)
		}
	}
}

func TestConcurrentSendAsyncSharesOnePool(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)

	results := make([]chan error, 10)
	for i := range results {
		results[i] = m.SendAsync()
	}
	for i, r := range results {
		if err := <-r; err != nil {
			t.Errorf("SendAsync #%d error = %v", i, err)
		}
	}
	if n := len(server.getData()); n != 10 {
		t.Errorf("server accepted %d messages, want 10", n)
	}
}

func TestSendDoesNotWriteIntoRecipientSlices(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	to := make([]string, 1, 4)
	to[0] = "recipient@example.com"
	m.To = to
	m.Cc = []string{"cc@example.com"}

	if err := m.Send(); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if spare := to[:2][1]; spare != "" {
		t.Errorf("Send wrote %q into the spare capacity of To", spare)
	}
}

func TestPoolReplacedWhenSettingsChange(t *testing.T) {
	first := newMockSMTPServer(t)
	defer first.close()
	second := newMockSMTPServer(t)
	defer second.close()
	m := newTestMail(t, first)

	if err := m.Send(); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	host, port, _ := net.SplitHostPort(second.addr())
	m.SetHost(host).SetPort(port)
	if err := m.Send(); err != nil {
		t.Fatalf("Send() after SetHost error = %v", err)
	}
	if a, b := len(first.getData()), len(second.getData()); a != 1 || b != 1 {
		t.Errorf("messages: first server %d, second %d; want 1 and 1", a, b)
	}
}

func TestLocalNameSentInEHLO(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	m.SetLocalName("mail.example.com")

	if err := m.Send(); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if !slices.Contains(server.getCommands(), "EHLO mail.example.com") {
		t.Errorf("commands = %v, want EHLO mail.example.com", server.getCommands())
	}
}

func TestSendHtmlEscapesData(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	path := filepath.Join(t.TempDir(), "confirm.html")
	if err := os.WriteFile(path, []byte(`<p>Merhaba {{.Name}}</p>`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := m.SendHtml(path, map[string]any{"Name": `<a href="https://evil.example">Ayşe</a>`}); err != nil {
		t.Fatalf("SendHtml() error = %v", err)
	}
	parts := messageParts(t, onlyMessage(t, server))
	body := string(parts[0].body)
	if strings.Contains(body, "<a href") || !strings.Contains(body, "&lt;a href=") {
		t.Errorf("body = %q, want the data escaped", body)
	}
}

// testCertificate returns a self-signed certificate for 127.0.0.1 and a pool that trusts it.
func testCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "gomail test"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, roots
}

func TestTLSVerificationIsOnByDefault(t *testing.T) {
	m := &Mail{Host: "smtp.example.com"}
	m.SetTLSConfig(&TLSConfig{StartTLS: true})
	cfg := m.stdTLSConfig()
	if cfg.InsecureSkipVerify {
		t.Error("InsecureSkipVerify is on by default")
	}
	if cfg.ServerName != "smtp.example.com" {
		t.Errorf("ServerName = %q, want the host", cfg.ServerName)
	}
}

func TestTLS(t *testing.T) {
	cert, roots := testCertificate(t)
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}}

	for _, startTLS := range []bool{true, false} {
		name := "direct TLS"
		if startTLS {
			name = "STARTTLS"
		}
		t.Run(name+" rejects an untrusted certificate", func(t *testing.T) {
			server := newMockSMTPServerWith(t, mockOptions{tlsConfig: serverTLS, startTLS: startTLS})
			defer server.close()
			m := newTestMail(t, server)
			m.SetTLSConfig(&TLSConfig{StartTLS: startTLS})

			var unknownAuthority x509.UnknownAuthorityError
			if err := m.Send(); !errors.As(err, &unknownAuthority) {
				t.Fatalf("Send() error = %v, want an unknown authority error", err)
			}
			if slices.ContainsFunc(server.getCommands(), func(c string) bool { return strings.HasPrefix(c, "AUTH") }) {
				t.Error("credentials were sent although TLS failed")
			}
		})
		t.Run(name+" with trusted roots", func(t *testing.T) {
			server := newMockSMTPServerWith(t, mockOptions{tlsConfig: serverTLS, startTLS: startTLS})
			defer server.close()
			m := newTestMail(t, server)
			m.SetTLSConfig(&TLSConfig{StartTLS: startTLS, RootCAs: roots})

			if err := m.Send(); err != nil {
				t.Fatalf("Send() error = %v", err)
			}
			onlyMessage(t, server)
		})
	}
}
