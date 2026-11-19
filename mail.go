package gomail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Mail represents an email message with all its configuration.
//
// Send, SendContext and SendAsync may be called concurrently on one Mail as long as no
// field is changed meanwhile; the setters are not synchronised.
type Mail struct {
	From    string
	Name    string
	Host    string
	Port    string
	User    string
	Pass    string `json:"-"` // Password will be omitted from JSON
	Subject string
	// Content is the body. It is HTML unless ContentType is TextPlain or TextMarkdown.
	Content string
	// PlainText is an optional text alternative to an HTML Content; when set, the body is
	// sent as multipart/alternative. It is ignored when Content is not HTML.
	PlainText string
	To        []string
	Cc        []string
	// Bcc recipients receive the message but never appear in its headers.
	Bcc         []string
	Attachments map[string][]byte
	// Timeout bounds the dial, the SMTP handshake (greeting, EHLO, STARTTLS, AUTH), the NOOP
	// check of a pooled connection and RSET. Zero means 5 seconds. The message transfer itself
	// is bounded only by the context given to SendContext.
	Timeout   time.Duration
	KeepAlive time.Duration
	// LocalName is sent in EHLO. Empty means "localhost", the net/smtp default.
	LocalName         string
	pool              *Pool
	poolKey           poolKey
	poolSize          int
	streamAttachments []AttachmentReader
	tlsConfig         *TLSConfig
	rateLimiter       *time.Ticker
	mu                sync.Mutex // guards pool, poolKey and rateLimiter
	ContentType       ContentType
	TemplateEngine    *TemplateEngine
	templateCache     map[string]*template.Template
	templateMutex     sync.RWMutex
}

// poolKey holds the settings a pooled connection was opened with; a change replaces the pool.
type poolKey struct {
	host, port, user, pass, localName string
	tls                               *TLSConfig
}

// SetFrom sets the sender's email address
func (m *Mail) SetFrom(from string) *Mail {
	m.From = from
	return m
}

// SetName sets the sender's name
func (m *Mail) SetName(name string) *Mail {
	m.Name = name
	return m
}

// SetHost sets the SMTP server host
func (m *Mail) SetHost(host string) *Mail {
	m.Host = host
	return m
}

// SetPort sets the SMTP server port
func (m *Mail) SetPort(port string) *Mail {
	m.Port = port
	return m
}

// SetUser sets the SMTP server username
func (m *Mail) SetUser(user string) *Mail {
	m.User = user
	return m
}

// SetPass sets the SMTP server password
func (m *Mail) SetPass(pass string) *Mail {
	m.Pass = pass
	return m
}

// SetSubject sets the email subject
func (m *Mail) SetSubject(subject string) *Mail {
	m.Subject = subject
	return m
}

// SetContent sets the email content
func (m *Mail) SetContent(content string) *Mail {
	m.Content = content
	return m
}

// SetPlainText sets a plain-text alternative to the HTML content.
func (m *Mail) SetPlainText(text string) *Mail {
	m.PlainText = text
	return m
}

// SetLocalName sets the host name sent in EHLO.
func (m *Mail) SetLocalName(name string) *Mail {
	m.LocalName = name
	return m
}

// SetTo sets the email recipients
func (m *Mail) SetTo(to ...string) *Mail {
	m.To = to
	return m
}

// SetCc sets the email CC recipients
func (m *Mail) SetCc(cc ...string) *Mail {
	m.Cc = cc
	return m
}

// SetBcc sets the email BCC recipients
func (m *Mail) SetBcc(bcc ...string) *Mail {
	m.Bcc = bcc
	return m
}

// SetTimeout sets the per-step connection timeout; see the Timeout field.
func (m *Mail) SetTimeout(timeout time.Duration) *Mail {
	m.Timeout = timeout
	return m
}

// SetKeepAlive sets the keep-alive duration
func (m *Mail) SetKeepAlive(keepAlive time.Duration) *Mail {
	m.KeepAlive = keepAlive
	return m
}

// SetAttachment sets the email attachments
func (m *Mail) SetAttachment(attachments map[string][]byte) *Mail {
	m.Attachments = attachments
	return m
}

// SetPoolSize sets the connection pool size
func (m *Mail) SetPoolSize(size int) *Mail {
	m.poolSize = size
	return m
}

// Send sends the email. It is SendContext with context.Background().
func (m *Mail) Send() error {
	return m.SendContext(context.Background())
}

// SendHtml renders the HTML template file with data (html/template, so data is escaped)
// and sends the result with up to 3 attempts.
func (m *Mail) SendHtml(filePath string, data map[string]any) error {
	content, err := SimpleRenderTemplate(filePath, data)
	if err != nil {
		return err
	}
	m.Content = content
	return m.SendWithRetry(3)
}

// SendWithRetry is SendWithRetryContext with context.Background().
func (m *Mail) SendWithRetry(maxRetries int) error {
	return m.SendWithRetryContext(context.Background(), maxRetries)
}

// SendWithRetryContext makes up to maxRetries attempts, waiting 2s, 4s, 8s... between them.
// It stops early on errors a retry cannot fix: validation errors and 5xx server replies.
// Seekable stream attachments are rewound before a retry; with a non-seekable one there is
// no retry, because its data was consumed by the first attempt.
// A network error after the message was sent may hide an accepted message, so a retry can
// deliver it twice.
func (m *Mail) SendWithRetryContext(ctx context.Context, maxRetries int) error {
	if maxRetries < 1 {
		return fmt.Errorf("failed to send email after %d attempts", maxRetries)
	}
	offsets, rewindable := m.streamOffsets()

	attempts := 0
	var lastErr error
	for attempts < maxRetries {
		if attempts > 0 {
			if !rewindable {
				break
			}
			wait := time.NewTimer(time.Duration(1<<uint(attempts)) * time.Second)
			select {
			case <-ctx.Done():
				wait.Stop()
				return withContext(ctx, fmt.Errorf("failed to send email after %d attempts, last error: %w", attempts, lastErr))
			case <-wait.C:
			}
			if err := m.rewindStreams(offsets); err != nil {
				return fmt.Errorf("failed to send email after %d attempts, rewinding attachment: %w", attempts, err)
			}
		}
		attempts++
		lastErr = m.SendContext(ctx)
		if lastErr == nil {
			return nil
		}
		if isPermanent(lastErr) || ctx.Err() != nil {
			break
		}
	}
	return fmt.Errorf("failed to send email after %d attempts, last error: %w", attempts, lastErr)
}

// isPermanent reports errors that a retry cannot fix.
func isPermanent(err error) bool {
	if errors.Is(err, ErrMissingParameter) || errors.Is(err, ErrInvalidAddress) || errors.Is(err, ErrInvalidHeader) {
		return true
	}
	var reply *textproto.Error
	return errors.As(err, &reply) && reply.Code >= 500
}

// streamOffsets records where each stream attachment starts; ok is false if one cannot seek.
func (m *Mail) streamOffsets() (offsets []int64, ok bool) {
	offsets = make([]int64, len(m.streamAttachments))
	for i, a := range m.streamAttachments {
		s, isSeeker := a.Reader.(io.Seeker)
		if !isSeeker {
			return nil, false
		}
		off, err := s.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, false
		}
		offsets[i] = off
	}
	return offsets, true
}

func (m *Mail) rewindStreams(offsets []int64) error {
	for i, a := range m.streamAttachments {
		if _, err := a.Reader.(io.Seeker).Seek(offsets[i], io.SeekStart); err != nil {
			return err
		}
	}
	return nil
}

// SendContext sends the email. ctx's deadline is applied to the SMTP connection and the
// send aborts when ctx is done. A nil error means the server accepted the message.
func (m *Mail) SendContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.check(); err != nil {
		return err
	}

	m.mu.Lock()
	limiter := m.rateLimiter
	m.mu.Unlock()
	if limiter != nil {
		select {
		case <-limiter.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	pool, err := m.getPool()
	if err != nil {
		return err
	}
	pc, err := pool.getConnection(ctx)
	if err != nil {
		return withContext(ctx, err)
	}

	deadline, _ := ctx.Deadline()
	release := guard(ctx, pc.conn, deadline)
	resettable, err := m.transmit(pc.client)
	if !release() {
		// ctx fired during the exchange: the deadline is poisoned and the session state unknown.
		pc.close()
		if err == nil {
			return nil // the server accepted the message before ctx fired
		}
		return withContext(ctx, err)
	}
	pool.finish(pc, err, resettable)
	return withContext(ctx, err)
}

// getPool returns the pool, replacing it when the connection settings changed since it was made.
func (m *Mail) getPool() (*Pool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := poolKey{host: m.Host, port: m.Port, user: m.User, pass: m.Pass, localName: m.LocalName, tls: m.tlsConfig}
	if m.pool != nil && m.poolKey != key {
		m.pool.Close()
		m.pool = nil
	}
	if m.pool == nil {
		pool, err := NewPool(m, m.poolSize)
		if err != nil {
			return nil, err
		}
		m.pool, m.poolKey = pool, key
	}
	return m.pool, nil
}

// transmit runs one SMTP transaction. resettable is false once the body is partly written:
// the terminator is then never sent, so closing the connection makes the server discard
// the partial message, and RSET cannot be sent inside DATA.
func (m *Mail) transmit(client *smtp.Client) (resettable bool, err error) {
	if err := client.Mail(m.From); err != nil {
		return true, err
	}
	for _, rcpt := range m.recipients() {
		if err := client.Rcpt(rcpt); err != nil {
			return true, err
		}
	}
	w, err := client.Data()
	if err != nil {
		return true, err
	}
	if err := m.writeMessage(w); err != nil {
		return false, err
	}
	// Close sends the terminator; its error is the server's verdict on the message.
	return true, w.Close()
}

func (m *Mail) recipients() []string {
	all := make([]string, 0, len(m.To)+len(m.Cc)+len(m.Bcc))
	all = append(all, m.To...)
	all = append(all, m.Cc...)
	return append(all, m.Bcc...)
}

// writeMessage writes headers, body and attachments, then closes the multipart writer.
func (m *Mail) writeMessage(w io.Writer) error {
	mw := multipart.NewWriter(w)
	if _, err := io.WriteString(w, m.headerBlock(mw.Boundary())); err != nil {
		return err
	}
	if err := m.writeBody(mw); err != nil {
		return err
	}
	for name, data := range m.Attachments {
		if err := writeAttachment(mw, name, bytes.NewReader(data)); err != nil {
			return err
		}
	}
	for _, a := range m.streamAttachments {
		if err := writeAttachment(mw, a.Name, a.Reader); err != nil {
			return err
		}
	}
	return mw.Close()
}

// headerBlock renders the top-level headers. Bcc is never written: blind recipients only
// appear in RCPT TO.
func (m *Mail) headerBlock(boundary string) string {
	var b strings.Builder
	h := func(name, value string) {
		b.WriteString(name)
		b.WriteString(": ")
		b.WriteString(value)
		b.WriteString("\r\n")
	}
	h("From", (&mail.Address{Name: m.Name, Address: m.From}).String())
	h("To", formatAddressList(m.To))
	if len(m.Cc) > 0 {
		h("Cc", formatAddressList(m.Cc))
	}
	h("Subject", encodeHeaderText(m.Subject))
	h("Date", time.Now().Format(time.RFC1123Z))
	h("Message-ID", "<"+rand.Text()+"@"+m.From[strings.LastIndex(m.From, "@")+1:]+">")
	h("MIME-Version", "1.0")
	h("Content-Type", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": boundary}))
	b.WriteString("\r\n")
	return b.String()
}

// formatAddressList folds after each address so a long list stays under the line limit.
func formatAddressList(addrs []string) string {
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = (&mail.Address{Address: a}).String()
	}
	return strings.Join(parts, ",\r\n ")
}

// encodeHeaderText RFC 2047-encodes s and folds between encoded words.
func encodeHeaderText(s string) string {
	return strings.ReplaceAll(mime.QEncoding.Encode("utf-8", s), "?= =?", "?=\r\n =?")
}

const (
	htmlContentType  = "text/html; charset=UTF-8"
	plainContentType = "text/plain; charset=UTF-8"
)

func (m *Mail) writeBody(mw *multipart.Writer) error {
	if m.ContentType == TextPlain || m.ContentType == TextMarkdown {
		return writeQuotedPart(mw, plainContentType, m.Content)
	}
	if m.PlainText == "" {
		return writeQuotedPart(mw, htmlContentType, m.Content)
	}

	altBoundary := multipart.NewWriter(io.Discard).Boundary()
	part, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type": {mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": altBoundary})},
	})
	if err != nil {
		return err
	}
	alt := multipart.NewWriter(part)
	if err := alt.SetBoundary(altBoundary); err != nil {
		return err
	}
	// RFC 2046: the preferred (richest) alternative comes last.
	if err := writeQuotedPart(alt, plainContentType, m.PlainText); err != nil {
		return err
	}
	if err := writeQuotedPart(alt, htmlContentType, m.Content); err != nil {
		return err
	}
	return alt.Close()
}

// writeQuotedPart writes body quoted-printable, which keeps every line under 77 octets
// (RFC 5322 caps a line at 998).
func writeQuotedPart(mw *multipart.Writer, contentType, body string) error {
	part, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {contentType},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return err
	}
	qp := quotedprintable.NewWriter(part)
	if _, err := io.WriteString(qp, body); err != nil {
		return err
	}
	return qp.Close()
}

// writeAttachment writes r base64-encoded. FormatMediaType encodes a non-ASCII filename per RFC 2231.
func writeAttachment(mw *multipart.Writer, name string, r io.Reader) error {
	part, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"application/octet-stream"},
		"Content-Transfer-Encoding": {"base64"},
		"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": name})},
	})
	if err != nil {
		return err
	}
	enc := base64.NewEncoder(base64.StdEncoding, &lineWrapper{w: part})
	if _, err := io.Copy(enc, r); err != nil {
		return err
	}
	return enc.Close()
}

// base64LineLength is the RFC 2045 §6.8 limit for base64 lines.
const base64LineLength = 76

// lineWrapper inserts CRLF every base64LineLength bytes.
type lineWrapper struct {
	w   io.Writer
	col int
}

func (l *lineWrapper) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		chunk := min(base64LineLength-l.col, len(p))
		if _, err := l.w.Write(p[:chunk]); err != nil {
			return n, err
		}
		n += chunk
		l.col += chunk
		p = p[chunk:]
		if l.col == base64LineLength {
			if _, err := io.WriteString(l.w, "\r\n"); err != nil {
				return n, err
			}
			l.col = 0
		}
	}
	return n, nil
}

func (m *Mail) stdTLSConfig() *tls.Config {
	c := m.tlsConfig
	serverName := c.ServerName
	if serverName == "" {
		serverName = m.Host
	}
	return &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: c.InsecureSkipVerify,
		Certificates:       c.Certificates,
		RootCAs:            c.RootCAs,
	}
}

// validate reports whether check passes.
func (m *Mail) validate() bool {
	return m.check() == nil
}

// check validates the message before any connection is made. Its errors name the field,
// never the value.
func (m *Mail) check() error {
	if m.From == "" || m.Name == "" || m.Host == "" || m.Port == "" ||
		m.User == "" || m.Pass == "" || m.Subject == "" || m.Content == "" ||
		len(m.To) == 0 {
		return ErrMissingParameter
	}
	for i, a := range m.streamAttachments {
		if a.Reader == nil {
			return fmt.Errorf("%w: stream attachment %d has no reader", ErrMissingParameter, i)
		}
	}
	if err := m.checkHeaderValues(); err != nil {
		return err
	}
	if !m.isEmailValid(m.From) {
		return fmt.Errorf("%w: From", ErrInvalidAddress)
	}
	for _, list := range []struct {
		field string
		addrs []string
	}{{"To", m.To}, {"Cc", m.Cc}, {"Bcc", m.Bcc}} {
		for i, a := range list.addrs {
			if !m.isEmailValid(a) {
				return fmt.Errorf("%w: %s[%d]", ErrInvalidAddress, list.field, i)
			}
		}
	}
	return nil
}

// checkHeaderValues refuses CR or LF in any value that reaches a header or an SMTP command.
func (m *Mail) checkHeaderValues() error {
	for _, f := range []struct{ field, value string }{{"From", m.From}, {"Name", m.Name}, {"Subject", m.Subject}} {
		if hasLineBreak(f.value) {
			return fmt.Errorf("%w: %s", ErrInvalidHeader, f.field)
		}
	}
	for _, list := range []struct {
		field string
		addrs []string
	}{{"To", m.To}, {"Cc", m.Cc}, {"Bcc", m.Bcc}} {
		for i, a := range list.addrs {
			if hasLineBreak(a) {
				return fmt.Errorf("%w: %s[%d]", ErrInvalidHeader, list.field, i)
			}
		}
	}
	for name := range m.Attachments {
		if hasLineBreak(name) {
			return fmt.Errorf("%w: attachment filename", ErrInvalidHeader)
		}
	}
	for i, a := range m.streamAttachments {
		if hasLineBreak(a.Name) {
			return fmt.Errorf("%w: stream attachment %d filename", ErrInvalidHeader, i)
		}
	}
	return nil
}

func hasLineBreak(s string) bool {
	return strings.ContainsAny(s, "\r\n")
}

var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)

// isEmailValid checks if the email address format is valid
func (m *Mail) isEmailValid(email string) bool {
	return emailRegex.MatchString(email)
}

// getTimeout returns the timeout duration with a default of 5 seconds
func (m *Mail) getTimeout() time.Duration {
	if m.Timeout == 0 {
		return 5 * time.Second
	}
	return m.Timeout
}

// getKeepAlive returns the keep-alive duration with a default of 10 seconds
func (m *Mail) getKeepAlive() time.Duration {
	if m.KeepAlive == 0 {
		return 10 * time.Second
	}
	return m.KeepAlive
}

// SendAsync sends the email asynchronously and returns a channel for the result
func (m *Mail) SendAsync() chan error {
	result := make(chan error, 1)
	go func() {
		result <- m.Send()
		close(result)
	}()
	return result
}

// SetStreamAttachment sets streaming attachments for the email
func (m *Mail) SetStreamAttachment(attachments []AttachmentReader) *Mail {
	m.streamAttachments = attachments
	return m
}

// SetTLSConfig sets the TLS configuration
func (m *Mail) SetTLSConfig(config *TLSConfig) *Mail {
	m.tlsConfig = config
	return m
}

// RateLimit represents rate limiting configuration
type RateLimit struct {
	Enabled   bool
	PerSecond int
}

// SetRateLimit configures rate limiting
func (m *Mail) SetRateLimit(limit *RateLimit) *Mail {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit != nil && limit.Enabled {
		if limit.PerSecond <= 0 {
			// Invalid rate limit, disable it
			if m.rateLimiter != nil {
				m.rateLimiter.Stop()
				m.rateLimiter = nil
			}
			return m
		}
		interval := time.Second / time.Duration(limit.PerSecond)
		if m.rateLimiter != nil {
			m.rateLimiter.Stop()
		}
		m.rateLimiter = time.NewTicker(interval)
	} else {
		if m.rateLimiter != nil {
			m.rateLimiter.Stop()
			m.rateLimiter = nil
		}
	}
	return m
}

// SetTemplateEngine configures the template engine
func (m *Mail) SetTemplateEngine(engine *TemplateEngine) *Mail {
	m.TemplateEngine = engine
	return m
}

// SetContentType sets the content type of the email
func (m *Mail) SetContentType(contentType ContentType) *Mail {
	m.ContentType = contentType
	return m
}

// RenderTemplate renders BaseDir/name+DefaultExt with data into Content. It uses
// html/template, so data is HTML-escaped; wrap trusted markup in template.HTML.
func (m *Mail) RenderTemplate(name string, data any) error {
	if m.TemplateEngine == nil {
		return errors.New("template engine not configured")
	}

	m.templateMutex.RLock()
	tmpl, exists := m.templateCache[name]
	m.templateMutex.RUnlock()

	if !exists {
		file := name + m.TemplateEngine.DefaultExt
		if !filepath.IsLocal(file) {
			return fmt.Errorf("gomail: template %q is outside BaseDir", name)
		}
		filePath := filepath.Join(m.TemplateEngine.BaseDir, file)
		// ParseFiles names the template after the file's base name; New must use the same name
		// or Execute runs an empty template.
		var err error
		tmpl, err = template.New(filepath.Base(filePath)).
			Funcs(template.FuncMap(m.TemplateEngine.FuncMap)).
			ParseFiles(filePath)
		if err != nil {
			return fmt.Errorf("failed to parse template: %w", err)
		}

		m.templateMutex.Lock()
		if m.templateCache == nil {
			m.templateCache = make(map[string]*template.Template)
		}
		m.templateCache[name] = tmpl
		m.templateMutex.Unlock()
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	m.Content = buf.String()
	return nil
}

// Close releases all resources held by the Mail instance (pool connections, rate limiter).
// Call this when the Mail instance is no longer needed.
func (m *Mail) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rateLimiter != nil {
		m.rateLimiter.Stop()
		m.rateLimiter = nil
	}
	if m.pool != nil {
		m.pool.Close()
		m.pool = nil
	}
}

// PreviewEmail returns a readable preview of the email for the sender. It lists Bcc, which
// the sent message never does.
func (m *Mail) PreviewEmail() (string, error) {
	if err := m.check(); err != nil {
		return "", err
	}

	var preview strings.Builder
	preview.WriteString(fmt.Sprintf("From: %s <%s>\n", m.Name, m.From))
	preview.WriteString(fmt.Sprintf("To: %s\n", strings.Join(m.To, ", ")))
	if len(m.Cc) > 0 {
		preview.WriteString(fmt.Sprintf("Cc: %s\n", strings.Join(m.Cc, ", ")))
	}
	if len(m.Bcc) > 0 {
		preview.WriteString(fmt.Sprintf("Bcc: %s\n", strings.Join(m.Bcc, ", ")))
	}
	preview.WriteString(fmt.Sprintf("Subject: %s\n\n", m.Subject))
	preview.WriteString(m.Content)

	return preview.String(), nil
}
