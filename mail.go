package gomail

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/smtp"
	"os"
	"regexp"
	"strings"
	"text/template"
	"time"
)

type Mail struct {
	From        string
	Name        string
	Host        string
	Port        string
	User        string
	Pass        string
	Subject     string
	Content     string
	To          []string
	Cc          []string
	Bcc         []string
	Attachments map[string][]byte
	Timeout     time.Duration
	KeepAlive   time.Duration
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

// SetTimeout
func (m *Mail) SetTimeout(timeout time.Duration) *Mail {
	m.Timeout = timeout
	return m
}

// SetKeepAlive
func (m *Mail) SetKeepAlive(keepAlive time.Duration) *Mail {
	m.KeepAlive = keepAlive
	return m
}

// SetAttachment sets the email attachments
func (m *Mail) SetAttachment(attachments map[string][]byte) *Mail {
	m.Attachments = attachments
	return m
}

func (m *Mail) Send() error {
	return m.send()
}

// SendFile loads an HTML file and renders it with dynamic data
func (m *Mail) SendHtml(filePath string, data map[string]any) error {
	content, err := RenderTemplate(filePath, data)
	if err != nil {
		return err
	}
	m.Content = content
	return m.send()
}

// RenderTemplate renders an HTML template with dynamic data
func RenderTemplate(filePath string, data map[string]any) (string, error) {
	fileContent, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	tmpl, err := template.New("email").Parse(string(fileContent))
	if err != nil {
		return "", err
	}

	var renderedContent bytes.Buffer
	if err := tmpl.Execute(&renderedContent, data); err != nil {
		return "", err
	}

	return renderedContent.String(), nil
}

// Send sends the email
func (m *Mail) send() error {
	if !m.validate() {
		return errors.New("missing parameter")
	}
	addr := fmt.Sprintf("%s:%s", m.Host, m.Port)

	// Create content
	var message strings.Builder
	message.WriteString(fmt.Sprintf("From: %s <%s>\n", m.Name, m.From))
	message.WriteString(fmt.Sprintf("To: %s\n", strings.Join(m.To, ", ")))
	message.WriteString(fmt.Sprintf("Cc: %s\n", strings.Join(m.Cc, ", ")))
	message.WriteString(fmt.Sprintf("Bcc: %s\n", strings.Join(m.Bcc, ", ")))
	message.WriteString(fmt.Sprintf("Subject: %s\n", m.Subject))
	message.WriteString("MIME-Version: 1.0\n")
	message.WriteString("Content-Type: multipart/mixed; boundary=BOUNDARY\n\n")

	// Add email content
	message.WriteString(fmt.Sprintf("--BOUNDARY\nContent-Type: text/html; charset=\"UTF-8\"\n\n%s\n\n", m.Content))

	// Add attachments
	for filename, data := range m.Attachments {
		message.WriteString(fmt.Sprintf("--BOUNDARY\nContent-Disposition: attachment; filename=\"%s\"\n", filename))
		message.WriteString("Content-Type: application/octet-stream\n\n")
		message.Write(data)
		message.WriteString("\n\n")
	}
	message.WriteString("--BOUNDARY--")

	// TLS configuration for connecting to SMTP server
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         m.Host,
	}

	// Connection timeout setting
	dialer := &net.Dialer{
		Timeout:   m.getTimeout() * time.Second,
		KeepAlive: m.getKeepAlive() * time.Second,
	}

	// Connecting to the SMTP server
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	if err != nil {
		return err
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		return err
	}
	defer client.Quit()

	// Authentication information
	auth := smtp.PlainAuth("", m.User, m.Pass, m.Host)

	if err := client.Auth(auth); err != nil {
		return err
	}

	// Email sending process
	if err := client.Mail(m.From); err != nil {
		return err
	}

	allRecipients := append(append(m.To, m.Cc...), m.Bcc...)
	for _, recipient := range allRecipients {
		if err := client.Rcpt(recipient); err != nil {
			return err
		}
	}

	// Start writing email content
	w, err := client.Data()
	if err != nil {
		return err
	}
	defer w.Close()

	// Write email header and body
	_, err = w.Write([]byte(message.String()))
	if err != nil {
		return err
	}

	return nil
}

func (m *Mail) validate() bool {
	if m.From == "" || m.Name == "" || m.Host == "" || m.Port == "" || m.User == "" || m.Pass == "" || m.Subject == "" || m.Content == "" || len(m.To) == 0 {
		return false
	}
	for _, email := range m.To {
		if !m.isEmailValid(email) {
			log.Printf("This email %s is not correct.\n", email)
			return false
		}
	}
	for _, email := range m.Cc {
		if !m.isEmailValid(email) {
			log.Printf("This email %s is not correct.\n", email)
			return false
		}
	}
	for _, email := range m.Bcc {
		if !m.isEmailValid(email) {
			log.Printf("This email %s is not correct.\n", email)
			return false
		}
	}
	return true
}

func (m *Mail) isEmailValid(email string) bool {
	regex := `^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`
	return regexp.MustCompile(regex).MatchString(email)
}

func (m *Mail) getTimeout() time.Duration {
	if m.Timeout == 0 {
		return 15
	}
	return m.Timeout
}

func (m *Mail) getKeepAlive() time.Duration {
	if m.KeepAlive == 0 {
		return 30
	}
	return m.KeepAlive
}
