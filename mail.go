package gomail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"io"
	"maps"
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
	maxConnections    int
	attachments       []Attachment
	streamAttachments []AttachmentReader
	tlsConfig         *TLSConfig
	rateLimiter       *rateLimiter
	slots             chan struct{} // the connection cap, shared by every pool this Mail makes
	mu                sync.Mutex    // guards pool, poolKey, slots and rateLimiter
	ContentType       ContentType
	TemplateEngine    *TemplateEngine
	templateCache     map[string]*template.Template
	templateMutex     sync.RWMutex
}

// poolKey holds the settings a pool was made with; a change replaces the pool.
type poolKey struct {
	host, port, user, pass, localName string
	size, maxConnections              int
	tls                               tlsKey
}

// tlsKey compares a TLSConfig by value, so editing a config already in use is noticed.
// Certificates and RootCAs compare by identity.
type tlsKey struct {
	set, startTLS, insecure bool
	serverName              string
	rootCAs                 *x509.CertPool
	firstCert               *tls.Certificate
	numCerts                int
}

func newTLSKey(c *TLSConfig) tlsKey {
	if c == nil {
		return tlsKey{}
	}
	k := tlsKey{
		set:        true,
		startTLS:   c.StartTLS,
		insecure:   c.InsecureSkipVerify,
		serverName: c.ServerName,
		rootCAs:    c.RootCAs,
		numCerts:   len(c.Certificates),
	}
	if len(c.Certificates) > 0 {
		k.firstCert = &c.Certificates[0]
	}
	return k
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

// SetPoolSize sets how many idle connections the pool keeps.
func (m *Mail) SetPoolSize(size int) *Mail {
	m.poolSize = size
	return m
}

// SetMaxConnections caps the connections open at once. A send beyond the cap waits for a
// free connection, honouring its context. Zero, the default, means no cap.
func (m *Mail) SetMaxConnections(n int) *Mail {
	m.maxConnections = n
	return m
}

// SetAttachments sets attachments with a content type, and inline files for the HTML body.
// They are sent in addition to SetAttachment and SetStreamAttachment.
func (m *Mail) SetAttachments(attachments []Attachment) *Mail {
	m.attachments = attachments
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

// isPermanent reports errors that a retry cannot fix: invalid input, a closed Mail, a 5xx
// reply, and configuration errors (certificate verification, TLS against a plain port,
// credentials refused by the client before AUTH).
func isPermanent(err error) bool {
	for _, target := range []error{ErrMissingParameter, ErrInvalidAddress, ErrInvalidHeader, ErrHeaderTooLong, ErrClosed} {
		if errors.Is(err, target) {
			return true
		}
	}
	var (
		reply     *textproto.Error
		verify    *tls.CertificateVerificationError
		unknownCA x509.UnknownAuthorityError
		hostname  x509.HostnameError
		invalid   x509.CertificateInvalidError
		record    tls.RecordHeaderError
		auth      *authConfigError
	)
	return (errors.As(err, &reply) && reply.Code >= 500) ||
		errors.As(err, &verify) || errors.As(err, &unknownCA) || errors.As(err, &hostname) ||
		errors.As(err, &invalid) || errors.As(err, &record) || errors.As(err, &auth)
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
	if err := m.waitRateLimit(ctx); err != nil {
		return err
	}

	// Headers are built before any connection, so a header error never touches the network.
	boundary := multipart.NewWriter(io.Discard).Boundary()
	header, err := m.headerBlock(boundary)
	if err != nil {
		return err
	}

	pool, err := m.getPool()
	if err != nil {
		return err
	}
	pc, err := pool.getConnection(ctx)
	if errors.Is(err, errPoolClosed) {
		// A settings change replaced the pool while this send waited for a slot.
		if pool, err = m.getPool(); err == nil {
			pc, err = pool.getConnection(ctx)
		}
	}
	if err != nil {
		return withContext(ctx, err)
	}

	deadline, _ := ctx.Deadline()
	release := guard(ctx, pc.conn, deadline)
	resettable, err := m.transmit(pc.client, header, boundary)
	if !release() {
		// ctx fired during the exchange: the deadline is poisoned and the session state unknown.
		pool.discard(pc)
		if err == nil {
			return nil // the server accepted the message before ctx fired
		}
		return withContext(ctx, err)
	}
	pool.finish(pc, err, resettable)
	return withContext(ctx, err)
}

// rateLimitWaitHook, set only by tests, runs when a send starts waiting for the limiter.
var rateLimitWaitHook func()

// waitRateLimit blocks until the rate limiter allows a send. A limiter replaced or turned
// off meanwhile is read again; one stopped by Close ends the wait with ErrClosed.
func (m *Mail) waitRateLimit(ctx context.Context) error {
	for {
		m.mu.Lock()
		limiter := m.rateLimiter
		m.mu.Unlock()
		if limiter == nil {
			return nil
		}
		if rateLimitWaitHook != nil {
			rateLimitWaitHook()
		}
		select {
		case <-limiter.ticker.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-limiter.done:
			if limiter.closed {
				return ErrClosed
			}
		}
	}
}

// getPool returns the pool, replacing it when the connection settings changed since it was made.
func (m *Mail) getPool() (*Pool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := poolKey{
		host: m.Host, port: m.Port, user: m.User, pass: m.Pass, localName: m.LocalName,
		size: m.poolSize, maxConnections: m.maxConnections, tls: newTLSKey(m.tlsConfig),
	}
	if m.pool != nil && m.poolKey != key {
		m.pool.Close()
		m.pool = nil
	}
	if m.pool == nil {
		pool, err := NewPool(m, m.poolSize)
		if err != nil {
			return nil, err
		}
		// One cap per Mail: connections still out from a replaced pool keep counting.
		switch {
		case m.maxConnections <= 0:
			m.slots = nil
		case cap(m.slots) != m.maxConnections:
			m.slots = make(chan struct{}, m.maxConnections)
		}
		pool.slots = m.slots
		m.pool, m.poolKey = pool, key
	}
	return m.pool, nil
}

// transmit runs one SMTP transaction. resettable is false once the body is partly written:
// the terminator is then never sent, so closing the connection makes the server discard
// the partial message, and RSET cannot be sent inside DATA.
func (m *Mail) transmit(client *smtp.Client, header, boundary string) (resettable bool, err error) {
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
	if err := m.writeMessage(w, header, boundary); err != nil {
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

// writeMessage writes the headers, body and attachments, then closes the multipart writer.
func (m *Mail) writeMessage(w io.Writer, header, boundary string) error {
	mw := multipart.NewWriter(w)
	if err := mw.SetBoundary(boundary); err != nil {
		return err
	}
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}
	if err := m.writeBody(mw); err != nil {
		return err
	}
	for name, data := range m.Attachments {
		if err := writeAttachment(mw, name, octetStream, false, bytes.NewReader(data)); err != nil {
			return err
		}
	}
	html := m.isHTML()
	for _, a := range m.attachments {
		if a.Inline && html {
			continue // written inside multipart/related by writeBody
		}
		if err := writeAttachment(mw, a.Name, a.contentType(), false, bytes.NewReader(a.Data)); err != nil {
			return err
		}
	}
	for _, a := range m.streamAttachments {
		if err := writeAttachment(mw, a.Name, octetStream, false, a.Reader); err != nil {
			return err
		}
	}
	return mw.Close()
}

// headerBlock renders the folded top-level headers. Bcc is never written: blind recipients
// only appear in RCPT TO.
func (m *Mail) headerBlock(boundary string) (string, error) {
	fields := [][2]string{
		{"From", (&mail.Address{Name: m.Name, Address: m.From}).String()},
		{"To", formatAddressList(m.To)},
	}
	if len(m.Cc) > 0 {
		fields = append(fields, [2]string{"Cc", formatAddressList(m.Cc)})
	}
	fields = append(fields,
		[2]string{"Subject", mime.QEncoding.Encode("utf-8", m.Subject)},
		[2]string{"Date", time.Now().Format(time.RFC1123Z)},
		[2]string{"Message-ID", "<" + rand.Text() + "@" + m.From[strings.LastIndex(m.From, "@")+1:] + ">"},
		[2]string{"MIME-Version", "1.0"},
		[2]string{"Content-Type", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": boundary})},
	)
	var b strings.Builder
	for _, f := range fields {
		line, err := foldHeader(f[0], f[1])
		if err != nil {
			return "", err
		}
		b.WriteString(line)
	}
	b.WriteString("\r\n")
	return b.String(), nil
}

// RFC 5322 §2.1.1: lines SHOULD stay within 78 octets and MUST stay within 998.
const (
	foldLineLength = 78
	maxLineLength  = 998
)

// foldHeader renders "name: value\r\n", inserting CRLF before a space wherever a line would
// pass foldLineLength. A word that cannot fit in maxLineLength is ErrHeaderTooLong.
func foldHeader(name, value string) (string, error) {
	var b strings.Builder
	b.WriteString(name)
	b.WriteString(":")
	line := len(name) + 1
	for i, word := range strings.Split(value, " ") {
		// Never fold before an empty word: a line of only whitespace is not allowed.
		if i > 0 && word != "" && line+1+len(word) > foldLineLength {
			b.WriteString("\r\n")
			line = 0
		}
		b.WriteString(" ")
		b.WriteString(word)
		line += 1 + len(word)
		if line > maxLineLength {
			return "", fmt.Errorf("%w: %s", ErrHeaderTooLong, name)
		}
	}
	b.WriteString("\r\n")
	return b.String(), nil
}

func formatAddressList(addrs []string) string {
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = (&mail.Address{Address: a}).String()
	}
	return strings.Join(parts, ", ")
}

const (
	htmlContentType  = "text/html; charset=UTF-8"
	plainContentType = "text/plain; charset=UTF-8"
	octetStream      = "application/octet-stream"
)

func (m *Mail) isHTML() bool {
	return m.ContentType != TextPlain && m.ContentType != TextMarkdown
}

// writeBody writes the text part. Inline attachments wrap an HTML body in multipart/related
// (RFC 2387) so the HTML can show them through cid: URLs.
func (m *Mail) writeBody(mw *multipart.Writer) error {
	if !m.isHTML() {
		return writeQuotedPart(mw, plainContentType, m.Content)
	}
	var inline []Attachment
	for _, a := range m.attachments {
		if a.Inline {
			inline = append(inline, a)
		}
	}
	if len(inline) == 0 {
		return m.writeHTML(mw)
	}

	rootType := "text/html"
	if m.PlainText != "" {
		rootType = "multipart/alternative"
	}
	related, err := createNested(mw, "multipart/related", map[string]string{"type": rootType})
	if err != nil {
		return err
	}
	if err := m.writeHTML(related); err != nil {
		return err
	}
	for _, a := range inline {
		if err := writeAttachment(related, a.Name, a.contentType(), true, bytes.NewReader(a.Data)); err != nil {
			return err
		}
	}
	return related.Close()
}

// writeHTML writes the HTML body, as multipart/alternative when PlainText is set.
func (m *Mail) writeHTML(mw *multipart.Writer) error {
	if m.PlainText == "" {
		return writeQuotedPart(mw, htmlContentType, m.Content)
	}
	alt, err := createNested(mw, "multipart/alternative", nil)
	if err != nil {
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

// createNested adds a multipart part of mediaType to mw and returns the writer for its parts.
func createNested(mw *multipart.Writer, mediaType string, params map[string]string) (*multipart.Writer, error) {
	boundary := multipart.NewWriter(io.Discard).Boundary()
	all := map[string]string{"boundary": boundary}
	maps.Copy(all, params)
	part, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {mime.FormatMediaType(mediaType, all)}})
	if err != nil {
		return nil, err
	}
	nested := multipart.NewWriter(part)
	return nested, nested.SetBoundary(boundary)
}

// contentType is ContentType, else the type of the Name's extension, else octet-stream.
func (a Attachment) contentType() string {
	if a.ContentType != "" {
		return a.ContentType
	}
	if t := mime.TypeByExtension(filepath.Ext(a.Name)); t != "" {
		return t
	}
	return octetStream
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

// writeAttachment writes r base64-encoded. FormatMediaType encodes a non-ASCII filename per
// RFC 2231. An inline part gets Content-ID <name>, which check has validated.
func writeAttachment(mw *multipart.Writer, name, contentType string, inline bool, r io.Reader) error {
	disposition := "attachment"
	header := textproto.MIMEHeader{
		"Content-Type":              {contentType},
		"Content-Transfer-Encoding": {"base64"},
	}
	if inline {
		disposition = "inline"
		header.Set("Content-ID", "<"+name+">")
	}
	header.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	part, err := mw.CreatePart(header)
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
	if err := m.checkAttachments(); err != nil {
		return err
	}
	// Renders the headers once to catch ErrHeaderTooLong; the boundary is a placeholder.
	_, err := m.headerBlock("x")
	return err
}

// checkHeaderValues refuses CR or LF in any value that reaches a header or an SMTP command.
func (m *Mail) checkHeaderValues() error {
	for _, f := range []struct{ field, value string }{{"From", m.From}, {"Name", m.Name}, {"Subject", m.Subject}} {
		if hasLineBreak(f.value) {
			return fmt.Errorf("%w: %s contains CR or LF", ErrInvalidHeader, f.field)
		}
	}
	for _, list := range []struct {
		field string
		addrs []string
	}{{"To", m.To}, {"Cc", m.Cc}, {"Bcc", m.Bcc}} {
		for i, a := range list.addrs {
			if hasLineBreak(a) {
				return fmt.Errorf("%w: %s[%d] contains CR or LF", ErrInvalidHeader, list.field, i)
			}
		}
	}
	for name := range m.Attachments {
		if hasLineBreak(name) {
			return fmt.Errorf("%w: an attachment filename contains CR or LF", ErrInvalidHeader)
		}
	}
	for i, a := range m.streamAttachments {
		if hasLineBreak(a.Name) {
			return fmt.Errorf("%w: stream attachment %d filename contains CR or LF", ErrInvalidHeader, i)
		}
	}
	for i, a := range m.attachments {
		if hasLineBreak(a.Name) || hasLineBreak(a.ContentType) {
			return fmt.Errorf("%w: attachment %d name or content type contains CR or LF", ErrInvalidHeader, i)
		}
	}
	return nil
}

// contentIDPattern keeps an inline name valid inside Content-ID <...> and a cid: URL.
var contentIDPattern = regexp.MustCompile(`^[A-Za-z0-9._@-]+$`)

// checkAttachments validates content types, inline names when the body is HTML, and the
// length of every part header.
func (m *Mail) checkAttachments() error {
	for name := range m.Attachments {
		if err := checkFilenameLength(name, "an attachment filename"); err != nil {
			return err
		}
	}
	for i, a := range m.streamAttachments {
		if err := checkFilenameLength(a.Name, fmt.Sprintf("stream attachment %d filename", i)); err != nil {
			return err
		}
	}
	seen := make(map[string]bool)
	for i, a := range m.attachments {
		if err := checkFilenameLength(a.Name, fmt.Sprintf("attachment %d name", i)); err != nil {
			return err
		}
		if a.ContentType != "" {
			if _, _, err := mime.ParseMediaType(a.ContentType); err != nil {
				return fmt.Errorf("%w: attachment %d content type is invalid", ErrInvalidHeader, i)
			}
			if partHeaderTooLong("Content-Type", a.ContentType) {
				return fmt.Errorf("%w: attachment %d content type", ErrHeaderTooLong, i)
			}
		}
		if !a.Inline || !m.isHTML() {
			continue
		}
		if !contentIDPattern.MatchString(a.Name) {
			return fmt.Errorf("%w: inline attachment %d name must use only letters, digits and . _ - @", ErrInvalidHeader, i)
		}
		if seen[a.Name] {
			return fmt.Errorf("%w: inline attachment %d repeats a name", ErrInvalidHeader, i)
		}
		seen[a.Name] = true
	}
	return nil
}

// checkFilenameLength refuses a filename whose Content-Disposition (RFC 2231 encoding takes
// up to 6 octets per non-ASCII rune) or Content-ID line would pass maxLineLength; part
// headers are not folded.
func checkFilenameLength(name, what string) error {
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": name})
	if partHeaderTooLong("Content-Disposition", disposition) || partHeaderTooLong("Content-ID", "<"+name+">") {
		return fmt.Errorf("%w: %s", ErrHeaderTooLong, what)
	}
	return nil
}

func partHeaderTooLong(name, value string) bool {
	return len(name)+len(": ")+len(value) > maxLineLength
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

// SetRateLimit configures rate limiting. A nil, disabled or non-positive limit turns it off.
// Sends already waiting move to the new limit, or go at once when it is turned off.
func (m *Mail) SetRateLimit(limit *RateLimit) *Mail {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rateLimiter != nil {
		m.rateLimiter.stop(false)
		m.rateLimiter = nil
	}
	if limit != nil && limit.Enabled && limit.PerSecond > 0 {
		m.rateLimiter = newRateLimiter(time.Second / time.Duration(limit.PerSecond))
	}
	return m
}

// rateLimiter wraps a Ticker so that stopping it also wakes waiting senders:
// Ticker.Stop never closes C.
type rateLimiter struct {
	ticker *time.Ticker
	done   chan struct{}
	closed bool // written before done is closed; true when Mail.Close stopped it
	once   sync.Once
}

func newRateLimiter(interval time.Duration) *rateLimiter {
	return &rateLimiter{ticker: time.NewTicker(interval), done: make(chan struct{})}
}

func (r *rateLimiter) stop(closed bool) {
	r.once.Do(func() {
		r.closed = closed
		r.ticker.Stop()
		close(r.done)
	})
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

// Close releases the pool connections and the rate limiter. A send waiting for the rate
// limiter or for a connection slot returns ErrClosed; a send started later opens a new
// pool, without rate limiting.
func (m *Mail) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rateLimiter != nil {
		m.rateLimiter.stop(true)
		m.rateLimiter = nil
	}
	if m.pool != nil {
		m.pool.closeWith(ErrClosed)
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
	fmt.Fprintf(&preview, "From: %s <%s>\n", m.Name, m.From)
	fmt.Fprintf(&preview, "To: %s\n", strings.Join(m.To, ", "))
	if len(m.Cc) > 0 {
		fmt.Fprintf(&preview, "Cc: %s\n", strings.Join(m.Cc, ", "))
	}
	if len(m.Bcc) > 0 {
		fmt.Fprintf(&preview, "Bcc: %s\n", strings.Join(m.Bcc, ", "))
	}
	fmt.Fprintf(&preview, "Subject: %s\n\n", m.Subject)
	preview.WriteString(m.Content)

	return preview.String(), nil
}
