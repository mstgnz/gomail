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
	// ErrInvalidHeader reports a value that cannot be written to a header or an SMTP command:
	// a CR or LF in From, Name, Subject, a recipient or an attachment filename, an inline
	// attachment name unusable as a Content-ID, or a malformed attachment content type.
	ErrInvalidHeader = errors.New("gomail: invalid header value")
	// ErrHeaderTooLong reports a header value with a word too long to fold under the
	// 998-octet line limit of RFC 5322.
	ErrHeaderTooLong = errors.New("gomail: header value too long")
	// ErrClosed is returned by a send that was waiting for the rate limiter or for a
	// connection slot when Close was called. Nothing was sent. A send started after Close
	// works as before.
	ErrClosed = errors.New("gomail: mail closed while the send was waiting")
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

// Attachment is a file sent with SetAttachments.
type Attachment struct {
	// Name is the filename. For an inline attachment it is also the Content-ID, so the
	// HTML refers to it as <img src="cid:Name">; it may then hold only letters, digits
	// and . _ - @.
	Name string
	// ContentType such as "image/png". Empty means detected from the Name's extension,
	// falling back to application/octet-stream.
	ContentType string
	Data        []byte
	// Inline shows the file inside the HTML body instead of as a download. It applies only
	// when the body is HTML; otherwise the file is attached normally.
	Inline bool
}

// AttachmentReader represents a streaming attachment
type AttachmentReader struct {
	Name   string
	Reader io.Reader
	Size   int64
}
