package gomail

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/textproto"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"time"
)

// Mail represents an email message with all its configuration
type Mail struct {
	From              string
	Name              string
	Host              string
	Port              string
	User              string
	Pass              string `json:"-"` // Password will be omitted from JSON
	Subject           string
	Content           string
	To                []string
	Cc                []string
	Bcc               []string
	Attachments       map[string][]byte
	Timeout           time.Duration
	KeepAlive         time.Duration
	pool              *Pool
	poolSize          int
	streamAttachments []AttachmentReader
	tlsConfig         *TLSConfig
	rateLimiter       *time.Ticker
	ContentType       ContentType
	TemplateEngine    *TemplateEngine
	templateCache     map[string]*template.Template
	templateMutex     sync.RWMutex
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

// SetTimeout sets the timeout duration
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

// Send initiates the email sending process
func (m *Mail) Send() error {
	return m.send()
}

// SendFile loads an HTML file and renders it with dynamic data
func (m *Mail) SendHtml(filePath string, data map[string]any) error {
	content, err := SimpleRenderTemplate(filePath, data)
	if err != nil {
		return err
	}
	m.Content = content
	return m.send()
}

// Send sends the email
func (m *Mail) send() error {
	if !m.validate() {
		return errors.New("missing parameter")
	}

	// Apply rate limiting if enabled
	if m.rateLimiter != nil {
		<-m.rateLimiter.C
	}

	// Initialize or use existing pool
	if m.pool == nil {
		pool, err := NewPool(m, m.poolSize)
		if err != nil {
			return fmt.Errorf("error creating pool: %v", err)
		}
		m.pool = pool
	}

	// Get connection from pool
	client, err := m.pool.getConnection()
	if err != nil {
		return err
	}
	defer m.pool.releaseConnection(client)

	// Send email process
	if err := client.Mail(m.From); err != nil {
		return err
	}

	allRecipients := append(append(m.To, m.Cc...), m.Bcc...)
	for _, recipient := range allRecipients {
		if err := client.Rcpt(recipient); err != nil {
			return err
		}
	}

	w, err := client.Data()
	if err != nil {
		return err
	}
	defer w.Close()

	// Write email content
	writer := multipart.NewWriter(w)
	defer writer.Close()

	// Write headers
	headers := fmt.Sprintf("From: %s <%s>\r\n"+
		"To: %s\r\n"+
		"Cc: %s\r\n"+
		"Bcc: %s\r\n"+
		"Subject: %s\r\n"+
		"MIME-Version: 1.0\r\n"+
		"Content-Type: multipart/mixed; boundary=%s\r\n\r\n",
		m.Name, m.From,
		strings.Join(m.To, ", "),
		strings.Join(m.Cc, ", "),
		strings.Join(m.Bcc, ", "),
		m.Subject,
		writer.Boundary())

	if _, err := w.Write([]byte(headers)); err != nil {
		return err
	}

	// Content section
	contentPart, err := writer.CreatePart(textproto.MIMEHeader{
		"Content-Type": []string{"text/html; charset=UTF-8"},
	})
	if err != nil {
		return err
	}
	if _, err := contentPart.Write([]byte(m.Content)); err != nil {
		return err
	}

	// Regular attachments
	for filename, data := range m.Attachments {
		attachmentPart, err := writer.CreatePart(textproto.MIMEHeader{
			"Content-Type":              []string{"application/octet-stream"},
			"Content-Transfer-Encoding": []string{"base64"},
			"Content-Disposition":       []string{fmt.Sprintf(`attachment; filename="%s"`, filename)},
		})
		if err != nil {
			return err
		}

		encoder := base64.NewEncoder(base64.StdEncoding, attachmentPart)
		if _, err := encoder.Write(data); err != nil {
			return err
		}
		encoder.Close()
	}

	// Streaming attachments
	for _, attachment := range m.streamAttachments {
		attachmentPart, err := writer.CreatePart(textproto.MIMEHeader{
			"Content-Type":              []string{"application/octet-stream"},
			"Content-Transfer-Encoding": []string{"base64"},
			"Content-Disposition":       []string{fmt.Sprintf(`attachment; filename="%s"`, attachment.Name)},
		})
		if err != nil {
			return err
		}

		encoder := base64.NewEncoder(base64.StdEncoding, attachmentPart)
		if _, err := io.Copy(encoder, attachment.Reader); err != nil {
			return err
		}
		encoder.Close()
	}

	return nil
}

// validate checks if all required fields are set and valid
func (m *Mail) validate() bool {
	// Check required fields
	if m.From == "" || m.Name == "" || m.Host == "" || m.Port == "" ||
		m.User == "" || m.Pass == "" || m.Subject == "" || m.Content == "" ||
		len(m.To) == 0 {
		return false
	}

	// Validate sender email
	if !m.isEmailValid(m.From) {
		log.Printf("Invalid sender email address: %s", m.From)
		return false
	}

	// Validate recipient emails
	for _, email := range m.To {
		if !m.isEmailValid(email) {
			log.Printf("Invalid recipient email address: %s", email)
			return false
		}
	}

	// Validate CC emails if present
	for _, email := range m.Cc {
		if !m.isEmailValid(email) {
			log.Printf("Invalid CC email address: %s", email)
			return false
		}
	}

	// Validate BCC emails if present
	for _, email := range m.Bcc {
		if !m.isEmailValid(email) {
			log.Printf("Invalid BCC email address: %s", email)
			return false
		}
	}

	return true
}

// isEmailValid checks if the email address format is valid
func (m *Mail) isEmailValid(email string) bool {
	regex := `^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`
	return regexp.MustCompile(regex).MatchString(email)
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
	if limit != nil && limit.Enabled {
		interval := time.Second / time.Duration(limit.PerSecond)
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

// RenderTemplate renders a template with the given data
func (m *Mail) RenderTemplate(name string, data any) error {
	if m.TemplateEngine == nil {
		return errors.New("template engine not configured")
	}

	m.templateMutex.RLock()
	tmpl, exists := m.templateCache[name]
	m.templateMutex.RUnlock()

	if !exists {
		// Load and cache template
		filePath := filepath.Join(m.TemplateEngine.BaseDir, name+m.TemplateEngine.DefaultExt)
		var err error
		tmpl, err = template.New(name).
			Funcs(m.TemplateEngine.FuncMap).
			ParseFiles(filePath)
		if err != nil {
			return fmt.Errorf("failed to parse template: %v", err)
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
		return fmt.Errorf("failed to execute template: %v", err)
	}

	m.Content = buf.String()
	return nil
}

// PreviewEmail returns a preview of the email content
func (m *Mail) PreviewEmail() (string, error) {
	if !m.validate() {
		return "", errors.New("missing parameter")
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
