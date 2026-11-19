package gomail

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"text/template"
	"time"
)

// Validation errors returned before any connection is made. Wrapped errors name the field
// (e.g. "To[1]") but never carry the value, so they are safe to log.
var (
	// ErrMissingParameter reports that a required field (From, Name, Host, Port, User, Pass,
	// Subject, Content or To) is empty.
	ErrMissingParameter = errors.New("missing parameter")
	// ErrInvalidAddress reports a malformed From, To, Cc or Bcc address.
	ErrInvalidAddress = errors.New("gomail: invalid email address")
	// ErrInvalidHeader reports a CR or LF in a value written to a header or an SMTP command:
	// From, Name, Subject, a recipient or an attachment filename.
	ErrInvalidHeader = errors.New("gomail: header value contains CR or LF")
)

const (
	TextPlain    ContentType = "text/plain"
	TextHTML     ContentType = "text/html"
	TextMarkdown ContentType = "text/markdown"
)

// Default configuration
const (
	// Deprecated: not applied by Mail. A zero Mail.Timeout means 5 seconds.
	DefaultTimeout = 30 * time.Second
	// Deprecated: not applied by Mail. A zero Mail.KeepAlive means 10 seconds.
	DefaultKeepAlive = 60 * time.Second
	// DefaultPoolSize is the number of idle connections a pool keeps when no size is set.
	DefaultPoolSize = 10
)

// TLSConfig represents TLS configuration options. Certificate verification is on unless
// InsecureSkipVerify is set; never set it in production.
type TLSConfig struct {
	// StartTLS upgrades a plain connection (usually port 587). False means direct TLS (port 465).
	StartTLS           bool
	InsecureSkipVerify bool
	// ServerName is the name verified in the server certificate. Empty means Mail.Host.
	ServerName   string
	Certificates []tls.Certificate
	// RootCAs verifies the server certificate. Nil means the system roots.
	RootCAs *x509.CertPool
}

// ContentType represents email content type
type ContentType string

// TemplateEngine represents template engine configuration
type TemplateEngine struct {
	BaseDir    string
	DefaultExt string
	FuncMap    template.FuncMap
}

// Attachment represents an email attachment with metadata
type Attachment struct {
	Name        string
	ContentType string
	Data        []byte
	Inline      bool
}

// AttachmentReader represents a streaming attachment
type AttachmentReader struct {
	Name   string
	Reader io.Reader
	Size   int64
}
