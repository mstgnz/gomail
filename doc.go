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

	err := mail.SetSubject("Test Email").
		SetContent("This is a test email.").
		SetTo("recipient@example.com").
		Send()

Features:

  - Connection pooling for better performance
  - HTML template support with caching
  - File attachments (regular and streaming)
  - Asynchronous sending
  - TLS support (STARTTLS and Direct TLS)
  - CC and BCC recipients
  - Rate limiting
  - Email preview
  - Configurable timeouts and keep-alive
  - Custom template functions

Connection Pooling:

The package implements connection pooling to reuse SMTP connections:

	mail.SetPoolSize(5) // Set pool size to 5 connections

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
