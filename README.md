# gomail
This is a Go package for sending emails. It provides functionality to send emails using the SMTP protocol with advanced features like connection pooling, rate limiting, and TLS support.

## Installation
To use the package, you need to have Go installed and Go modules enabled in your project. Then you can add the package to your project by running the following command:

```bash
go get -u github.com/mstgnz/gomail
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

### HTML Templates with Custom Functions
```go
// Configure template engine
engine := &TemplateEngine{
    BaseDir:    "templates",
    DefaultExt: ".html",
    FuncMap: template.FuncMap{
        "upper": strings.ToUpper,
        "formatDate": func(t time.Time) string {
            return t.Format("2006-01-02")
        },
    },
}
mail.SetTemplateEngine(engine)

// Render template with data
data := map[string]any{
    "Name": "John",
    "Date": time.Now(),
}
err := mail.RenderTemplate("welcome", data)
```

Example template (templates/welcome.html):
```html
<html>
<body>
    <h1>Welcome {{.Name}}!</h1>
    <p>Today is {{formatDate .Date}}</p>
    <p>Your name in uppercase: {{upper .Name}}</p>
</body>
</html>
```

### TLS Configuration
```go
// STARTTLS configuration
mail.SetTLSConfig(&TLSConfig{
    StartTLS:           true,
    InsecureSkipVerify: false,
    ServerName:         "smtp.example.com",
})

// Direct TLS configuration
mail.SetTLSConfig(&TLSConfig{
    StartTLS:           false,
    InsecureSkipVerify: false,
    ServerName:         "smtp.example.com",
})
```

### Rate Limiting
```go
// Limit to 10 emails per second
mail.SetRateLimit(&RateLimit{
    Enabled:   true,
    PerSecond: 10,
})

// Send multiple emails with rate limiting
for i := 0; i < 100; i++ {
    err := mail.SetSubject(fmt.Sprintf("Email %d", i)).
        SetContent("Rate limited email").
        SetTo("recipient@example.com").
        Send()
    if err != nil {
        log.Printf("Failed to send email %d: %v", i, err)
    }
}
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

err = mail.SetSubject("Email with Large Attachment").
    SetContent("Please find the attached file.").
    SetTo("recipient@example.com").
    SetStreamAttachment(attachments).
    Send()
```

### Email Preview
```go
// Preview email before sending
preview, err := mail.PreviewEmail()
if err != nil {
    log.Printf("Preview error: %v", err)
    return
}
fmt.Println("Email Preview:")
fmt.Println(preview)
```

### Error Handling
```go
// Basic error handling
err := mail.Send()
if err != nil {
    switch {
    case strings.Contains(err.Error(), "connection refused"):
        log.Printf("SMTP server is not accessible: %v", err)
    case strings.Contains(err.Error(), "invalid auth"):
        log.Printf("Authentication failed: %v", err)
    case strings.Contains(err.Error(), "invalid recipient"):
        log.Printf("Invalid recipient address: %v", err)
    default:
        log.Printf("Failed to send email: %v", err)
    }
}

// Async error handling with timeout
result := mail.SendAsync()
select {
case err := <-result:
    if err != nil {
        log.Printf("Failed to send email: %v", err)
    }
case <-time.After(30 * time.Second):
    log.Printf("Email sending timed out")
}
```

### Template Usage Examples
```go
// 1. Basic Template
engine := &TemplateEngine{
    BaseDir:    "templates",
    DefaultExt: ".html",
}
mail.SetTemplateEngine(engine)

// Basic template usage
data := map[string]any{
    "Name": "John",
    "Products": []string{"Product 1", "Product 2"},
    "Total": 99.99,
}
err := mail.RenderTemplate("order-confirmation", data)

// 2. Custom Template Functions
engine := &TemplateEngine{
    BaseDir:    "templates",
    DefaultExt: ".html",
    FuncMap: template.FuncMap{
        "upper": strings.ToUpper,
        "formatDate": func(t time.Time) string {
            return t.Format("2006-01-02")
        },
        "formatPrice": func(price float64) string {
            return fmt.Sprintf("$%.2f", price)
        },
        "safeHTML": func(s string) template.HTML {
            return template.HTML(s)
        },
    },
}
mail.SetTemplateEngine(engine)

// 3. Template with Layouts
// templates/layouts/base.html
/*
<!DOCTYPE html>
<html>
<head>
    <title>{{.Title}}</title>
</head>
<body>
    <header>{{template "header" .}}</header>
    <main>{{template "content" .}}</main>
    <footer>{{template "footer" .}}</footer>
</body>
</html>
*/

// templates/welcome.html
/*
{{define "header"}}
    <h1>Welcome {{.Name}}</h1>
{{end}}

{{define "content"}}
    <p>Today is {{formatDate .Date}}</p>
    <p>Your total: {{formatPrice .Total}}</p>
{{end}}

{{define "footer"}}
    <p>Contact us: support@example.com</p>
{{end}}
*/
```

### TLS Configuration Examples
```go
// 1. STARTTLS with Gmail
mail.SetTLSConfig(&TLSConfig{
    StartTLS:           true,
    InsecureSkipVerify: false,
    ServerName:         "smtp.gmail.com",
})

// 2. Direct TLS with custom certificates
cert, err := tls.LoadX509KeyPair("cert.pem", "key.pem")
if err != nil {
    log.Fatal(err)
}

mail.SetTLSConfig(&TLSConfig{
    StartTLS:           false,
    InsecureSkipVerify: false,
    ServerName:         "smtp.example.com",
    Certificates:       []tls.Certificate{cert},
})

// 3. TLS with custom verification
mail.SetTLSConfig(&TLSConfig{
    StartTLS:           true,
    InsecureSkipVerify: false,
    ServerName:         "smtp.example.com",
    MinVersion:         tls.VersionTLS12,
    MaxVersion:         tls.VersionTLS13,
})

// 4. Development/Testing TLS (insecure)
mail.SetTLSConfig(&TLSConfig{
    StartTLS:           true,
    InsecureSkipVerify: true, // Only for development!
    ServerName:         "localhost",
})
```

### Rate Limiting Examples
```go
// 1. Basic Rate Limiting
mail.SetRateLimit(&RateLimit{
    Enabled:   true,
    PerSecond: 10, // 10 emails per second
})

// 2. Burst Sending with Rate Limiting
emails := []struct {
    to      string
    subject string
    content string
}{
    {"user1@example.com", "Subject 1", "Content 1"},
    {"user2@example.com", "Subject 2", "Content 2"},
    // ... more emails
}

// Configure rate limiting
mail.SetRateLimit(&RateLimit{
    Enabled:   true,
    PerSecond: 5, // 5 emails per second
})

// Send emails with progress tracking
for i, email := range emails {
    err := mail.SetSubject(email.subject).
        SetContent(email.content).
        SetTo(email.to).
        Send()
    
    if err != nil {
        log.Printf("Failed to send email %d: %v", i+1, err)
        continue
    }
    
    log.Printf("Progress: %d/%d emails sent", i+1, len(emails))
}

// 3. Async Sending with Rate Limiting
results := make(chan error, len(emails))
for _, email := range emails {
    go func(e struct {
        to      string
        subject string
        content string
    }) {
        results <- mail.SetSubject(e.subject).
            SetContent(e.content).
            SetTo(e.to).
            Send()
    }(email)
}

// Collect results
for i := 0; i < len(emails); i++ {
    if err := <-results; err != nil {
        log.Printf("Email error: %v", err)
    }
}
```

### Error Handling Examples
```go
// 1. Comprehensive Error Handling
err := mail.Send()
if err != nil {
    switch {
    case strings.Contains(err.Error(), "connection refused"):
        log.Printf("SMTP server is not accessible: %v", err)
        // Retry with backup server
        mail.SetHost("backup-smtp.example.com")
        err = mail.Send()
        
    case strings.Contains(err.Error(), "invalid auth"):
        log.Printf("Authentication failed: %v", err)
        // Refresh credentials and retry
        mail.SetUser("new-user").SetPass("new-pass")
        err = mail.Send()
        
    case strings.Contains(err.Error(), "invalid recipient"):
        log.Printf("Invalid recipient address: %v", err)
        // Log invalid address for cleanup
        logInvalidAddress(mail.To[0])
        
    case strings.Contains(err.Error(), "timeout"):
        log.Printf("Connection timeout: %v", err)
        // Retry with increased timeout
        mail.SetTimeout(30 * time.Second)
        err = mail.Send()
        
    default:
        log.Printf("Unexpected error: %v", err)
    }
}

// 2. Retry Logic
maxRetries := 3
retryDelay := time.Second

for i := 0; i < maxRetries; i++ {
    err := mail.Send()
    if err == nil {
        break
    }
    
    log.Printf("Attempt %d failed: %v", i+1, err)
    if i < maxRetries-1 {
        time.Sleep(retryDelay * time.Duration(i+1))
    }
}

// 3. Async Error Handling with Context
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

result := mail.SendAsync()
select {
case err := <-result:
    if err != nil {
        log.Printf("Failed to send email: %v", err)
    }
case <-ctx.Done():
    log.Printf("Email sending timed out or cancelled")
case <-time.After(5 * time.Second):
    log.Printf("Email sending took too long")
}

// 4. Batch Error Handling
type EmailResult struct {
    To    string
    Error error
}

results := make(chan EmailResult, len(recipients))
for _, to := range recipients {
    go func(recipient string) {
        err := mail.SetTo(recipient).Send()
        results <- EmailResult{To: recipient, Error: err}
    }(to)
}

// Process results
successCount := 0
failureCount := 0
for i := 0; i < len(recipients); i++ {
    result := <-results
    if result.Error != nil {
        failureCount++
        log.Printf("Failed to send to %s: %v", result.To, result.Error)
    } else {
        successCount++
    }
}

log.Printf("Sending complete: %d successful, %d failed", successCount, failureCount)
```

## Contributing
Contributions are welcome! For any feedback, bug reports, or contributions, please submit an issue or pull request to the GitHub repository.

## License
This package is licensed under the MIT License. See the LICENSE file for more information.