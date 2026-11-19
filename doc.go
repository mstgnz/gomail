/*
Package gomail provides a simple and efficient way to send emails using SMTP protocol.
It supports various features including HTML templates, attachments, connection pooling,
rate limiting, TLS configuration, and asynchronous sending.

Basic Usage:

	mail := &gomail.Mail{
		From:    "sender@example.com",
		Name:    "Sender Name",
		Host:    "smtp.example.com",
		Port:    "587",
		User:    "username",
		Pass:    "password",
	}
	// Port 587 uses STARTTLS; without TLS the password is not sent to a remote server.
	mail.SetTLSConfig(&gomail.TLSConfig{StartTLS: true})

	err := mail.SetSubject("Test Email").
		SetContent("This is a test email.").
		SetTo("recipient@example.com").
		Send()

A nil error means the server accepted the message; a rejection is returned as a
*textproto.Error. The package never logs.

Context:

SendContext applies ctx's deadline to the SMTP connection and aborts when ctx is done.
Send is SendContext(context.Background()):

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := mail.SendContext(ctx)

Errors:

Validation fails before any connection with ErrMissingParameter, ErrInvalidAddress,
ErrInvalidHeader (CR or LF in a header value, a bad inline name or content type) or
ErrHeaderTooLong. They name the field, never the value. A send waiting for the rate
limiter or for a connection slot when Close is called returns ErrClosed.

Features:

  - Connection pooling with NOOP health checks
  - RFC 2047 encoded Subject and sender name, quoted-printable bodies
  - Optional plain-text alternative (SetPlainText)
  - HTML templates with html/template escaping and caching
  - File attachments (regular, streaming, typed) and inline images (SetAttachments)
  - Optional cap on concurrent connections (SetMaxConnections)
  - Asynchronous sending and retries
  - TLS support (STARTTLS and Direct TLS), verification on by default
  - CC and BCC recipients; Bcc never appears in the headers
  - Rate limiting
  - Email preview
  - Configurable timeouts and keep-alive
  - Custom template functions

Connection Pooling:

Connections are opened on first use and reused; an idle one is checked with NOOP first:

	mail.SetPoolSize(5) // Keep at most 5 idle connections

HTML Templates:

Send emails using HTML templates with support for custom functions:

	engine := &TemplateEngine{
		BaseDir:     "templates",
		DefaultExt:  ".html",
		FuncMap:     template.FuncMap{
			"upper": strings.ToUpper,
		},
	}
	mail.SetTemplateEngine(engine)
	mail.RenderTemplate("welcome", data)

Rate Limiting:

Control email sending rate:

	mail.SetRateLimit(&RateLimit{
		Enabled:   true,
		PerSecond: 2, // 2 emails per second
	})

TLS Configuration:

Configure TLS settings:

	mail.SetTLSConfig(&TLSConfig{
		StartTLS:           true,
		InsecureSkipVerify: false,
		ServerName:         "smtp.example.com",
	})

Asynchronous Sending:

Send emails asynchronously:

	result := mail.SetSubject("Async Email").
		SetContent("Content").
		SetTo("recipient@example.com").
		SendAsync()

	if err := <-result; err != nil {
		log.Printf("Failed to send: %v", err)
	}

Attachments:

Add file attachments:

	attachments := map[string][]byte{
		"file.txt": []byte("content"),
	}
	mail.SetAttachment(attachments)

For large files, use streaming attachments:

	file, _ := os.Open("large-file.zip")
	defer file.Close()

	attachments := []AttachmentReader{
		{
			Name:   "large-file.zip",
			Reader: file,
			Size:   fileInfo.Size(),
		},
	}
	mail.SetStreamAttachment(attachments)

Email Preview:

Preview email content before sending:

	preview, err := mail.PreviewEmail()
	if err != nil {
		log.Printf("Preview error: %v", err)
	}
	fmt.Println(preview)
*/
package gomail
