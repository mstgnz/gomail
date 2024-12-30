package gomail

import (
	"crypto/tls"
	"io"
	"text/template"
	"time"
)

const (
	TextPlain    ContentType = "text/plain"
	TextHTML     ContentType = "text/html"
	TextMarkdown ContentType = "text/markdown"
)

// Default configuration
const (
	DefaultTimeout   = 30 * time.Second
	DefaultKeepAlive = 60 * time.Second
	DefaultPoolSize  = 10
)

// TLSConfig represents TLS configuration options
type TLSConfig struct {
	StartTLS           bool
	InsecureSkipVerify bool
	ServerName         string
	Certificates       []tls.Certificate
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
