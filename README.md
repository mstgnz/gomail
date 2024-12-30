# gomail
This is a Go package for sending emails. It provides functionality to send emails using the SMTP protocol with advanced features like connection pooling, rate limiting, and TLS support.

## Installation
To use the package, you need to have Go installed and Go modules enabled in your project. Then you can add the package to your project by running the following command:

```bash
go get -u github.com/mstgnz/gomail
```

## Benchmarks
Performance benchmarks on Apple M1:

```
BenchmarkMailSend-8                    13111    83133 ns/op    14435 B/op    184 allocs/op
BenchmarkMailSendWithAttachments-8     13074    87134 ns/op    19552 B/op    234 allocs/op
BenchmarkMailSendAsync-8               12862    92055 ns/op    14660 B/op    187 allocs/op
```

- `MailSend`: Basic email sending without attachments
- `MailSendWithAttachments`: Email sending with file attachments
- `MailSendAsync`: Asynchronous email sending

The benchmarks show that:
- Basic email sending takes ~83μs per operation
- Adding attachments increases memory allocation by ~35% but only adds ~4μs to operation time
- Async sending has minimal overhead (~9μs) compared to synchronous sending

All operations maintain efficient memory usage with relatively low allocations.

## Basic Usage
```go
package main

import (
    "github.com/mstgnz/gomail"
)

func main() {
    // Create a Mail struct and set the necessary fields
    mail := &Mail{
        From:    "sender@example.com",
        Name:    "Sender Name",
        Host:    "smtp.example.com",
        Port:    "587",
        User:    "username",
        Pass:    "password",
    }

    // Send a simple email
    err := mail.SetSubject("Test Email").
        SetContent("This is a test email.").
        SetTo("recipient@example.com").
        Send()
    if err != nil {
        // Handle error
    }

    // Send an email with HTML template
    err = mail.SetSubject("Test Email").
        SetTo("recipient@example.com").
        SendHtml("templates/welcome.html", map[string]any{
            "Name": "John",
            "URL":  "https://example.com",
        })
    if err != nil {
        // Handle error
    }
}
```

## Advanced Features

### Connection Pooling
```go
mail := &Mail{
    From:     "sender@example.com",
    Name:     "Sender Name",
    Host:     "smtp.example.com",
    Port:     "587",
    User:     "username",
    Pass:     "password",
}

// Set connection pool size (default: 10)
mail.SetPoolSize(20)
```

### Asynchronous Email Sending
```go
// Send email asynchronously
result := mail.SetSubject("Test Email").
    SetContent("This is a test email.").
    SetTo("recipient@example.com").
    SendAsync()

// Check the result
if err := <-result; err != nil {
    log.Printf("Failed to send email: %v", err)
}
```

### Large File Attachments (Streaming)
```go
// Stream a large file
file, err := os.Open("large-file.zip")
if err != nil {
    log.Fatal(err)
}
defer file.Close()

fileInfo, err := file.Stat()
if err != nil {
    log.Fatal(err)
}

attachments := []AttachmentReader{
    {
        Name:   "large-file.zip",
        Reader: file,
        Size:   fileInfo.Size(),
    },
}

err = mail.SetSubject("Test Email with Large Attachment").
    SetContent("This is a test email with a large attachment.").
    SetTo("recipient@example.com").
    SetStreamAttachment(attachments).
    Send()
```

### Rate Limiting
```go
// Configure rate limiting
mail.SetRateLimit(&RateLimit{
    Enabled:   true,
    PerSecond: 2, // 2 emails per second
})
```

### TLS Configuration
```go
// Configure TLS settings
mail.SetTLSConfig(&TLSConfig{
    StartTLS:           true,
    InsecureSkipVerify: false,
    ServerName:         "smtp.example.com",
})
```

### Template Engine with Custom Functions
```go
// Configure template engine
engine := &TemplateEngine{
    BaseDir:    "templates",
    DefaultExt: ".html",
    FuncMap: template.FuncMap{
        "upper": strings.ToUpper,
    },
}
mail.SetTemplateEngine(engine)

// Render template
err := mail.RenderTemplate("welcome", data)
```

### Email Preview
```go
// Preview email before sending
preview, err := mail.PreviewEmail()
if err != nil {
    log.Printf("Preview error: %v", err)
}
fmt.Println(preview)
```

## Features
- SMTP protocol support
- Connection pooling for performance optimization
- HTML template support with custom functions
- File attachments (regular and streaming)
- Asynchronous email sending
- Rate limiting
- TLS support (STARTTLS and Direct TLS)
- CC and BCC recipients
- Email preview
- Configurable timeouts and keep-alive
- Template caching
- Comprehensive error handling

## Contributing
Contributions are welcome! For any feedback, bug reports, or contributions, please submit an issue or pull request to the GitHub repository.

## License
This package is licensed under the MIT License. See the LICENSE file for more information.