# gomail
This is a Go package for sending emails. It provides functionality to send emails using the SMTP protocol with advanced features like connection pooling, rate limiting, and TLS support.

## Installation
To use the package, you need to have Go installed and Go modules enabled in your project. Then you can add the package to your project by running the following command:

```bash
go get -u github.com/mstgnz/gomail
```

## Features
- SMTP protocol support, no dependencies outside the standard library
- `SendContext`: deadlines and cancellation on the SMTP connection
- A rejected message is an error: the server's answer to the message (250 or 4xx/5xx) is returned
- Connection pooling with health checks: idle connections are checked with NOOP and replaced when dead
- UTF-8 everywhere: Subject and sender name are RFC 2047 encoded, attachment names RFC 2231,
  bodies quoted-printable, so Turkish and other non-ASCII text arrive intact
- Header injection protection: CR or LF in a header value is refused; long headers are folded
- Bcc recipients never appear in the message headers
- Date and Message-ID headers
- HTML templates with automatic escaping (`html/template`) and custom functions
- Optional plain-text alternative (`multipart/alternative`)
- File attachments (regular, streaming, typed) and inline images (`cid:`)
- Optional cap on concurrent connections
- Asynchronous sending and retries with backoff
- Rate limiting
- TLS (STARTTLS and direct TLS) with certificate verification on by default
- No logging: every failure is returned as an error

## Benchmarks
Against the in-process test server, Apple M2 Max:

```
BenchmarkMailSend-12                    8569    138486 ns/op     7831 B/op    100 allocs/op
BenchmarkMailSendWithAttachments-12     7903    161349 ns/op    14048 B/op    172 allocs/op
BenchmarkMailSendAsync-12               8478    176748 ns/op     7999 B/op    103 allocs/op
```

Each send includes the NOOP check of the pooled connection, one extra round trip.

## Basic Usage
```go
package main

import (
    "github.com/mstgnz/gomail"
)

func main() {
    // Create a Mail struct and set the necessary fields
    mail := &gomail.Mail{
        From:    "sender@example.com",
        Name:    "Sender Name",
        Host:    "smtp.example.com",
        Port:    "587",
        User:    "username",
        Pass:    "password",
    }
    // Port 587 uses STARTTLS. Without TLS the password is not sent to a remote server.
    mail.SetTLSConfig(&gomail.TLSConfig{StartTLS: true})

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

`Send` returns nil only after the server accepted the message. A rejection (for example
`554` after DATA) is returned as an error, so a lost message is never silent.

### From a background worker: SendContext
```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

err := mail.SetSubject("Rezervasyonunuz onaylandı").
    SetContent("<p>Merhaba Ayşe, rezervasyonunuz onaylandı.</p>").
    SetPlainText("Merhaba Ayşe, rezervasyonunuz onaylandı.").
    SetTo("customer@example.com").
    SendContext(ctx)
```

`SendContext` applies the context's deadline to the SMTP connection and aborts when the
context is cancelled. `Send()` is `SendContext(context.Background())`: without a deadline
only the connection steps are bounded (by `Timeout`, default 5s), not the message
transfer, so prefer `SendContext` in servers and workers.

## Advanced Features

### Connection Pooling
```go
// Keep at most 20 idle connections (default: 10)
mail.SetPoolSize(20)
```

Connections are opened on first use, not up front. Before an idle connection is reused it
is checked with NOOP; a connection the server has closed is replaced, not returned as an
error. After a failed transaction the connection is reset with RSET, or closed when the
session state is unknown. Changing Host, Port, User, Pass, LocalName, the TLS settings,
the pool size or the connection cap starts a new pool on the next send. A `*TLSConfig` is
compared field by field; its `Certificates` slice and `RootCAs` pool by identity, so
replace them rather than mutating them.

### Connection cap
```go
// At most 5 connections open at once; extra sends wait for a free one.
mail.SetMaxConnections(5)
```

Most SMTP providers limit concurrent connections per account. With a cap, a send beyond
it waits for a free connection, honouring its context; `Close()` ends that wait with
`ErrClosed`. The cap belongs to the `Mail`, so connections still in use when the pool is
replaced keep counting. The default, 0, means no cap: N concurrent sends open up to N
connections.

### Concurrency
`Send`, `SendContext` and `SendAsync` can run concurrently on one `Mail` as long as no
field changes meanwhile. The setters are not synchronised: to send different messages in
parallel, use one `Mail` per goroutine.

### HTML Templates with Custom Functions
Templates are rendered with `html/template`, so data is HTML-escaped: a customer name such
as `<a href=...>` arrives as text, not as a link. Wrap markup you trust in `template.HTML`
(from `html/template`).

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
Certificate verification is on by default: `InsecureSkipVerify` is false unless you set it,
and `ServerName` defaults to `Host`. `net/smtp` also refuses to send the password over an
unencrypted connection, except to localhost, so a remote server needs a `TLSConfig`.

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

`Close()` wakes a send that is waiting for the rate limiter; it returns `ErrClosed` and
sends nothing. Calling `SetRateLimit` again moves waiting sends to the new rate, or lets
them go at once when the limit is turned off.

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

### Attachments with a content type, and inline images
```go
logo, _ := os.ReadFile("logo.png")
ticket, _ := os.ReadFile("ticket.pdf")

err := mail.SetContent(`<img src="cid:logo.png"><p>Rezervasyonunuz onaylandı.</p>`).
    SetAttachments([]gomail.Attachment{
        {Name: "logo.png", Data: logo, Inline: true}, // shown inside the HTML
        {Name: "ticket.pdf", Data: ticket},           // a normal download
        {Name: "match.ics", ContentType: "text/calendar; method=REQUEST", Data: ics},
    }).
    SendContext(ctx)
```

An empty `ContentType` is taken from the file extension, falling back to
`application/octet-stream`. An inline file is sent with `Content-ID: <Name>` inside a
`multipart/related` part, so its name may hold only letters, digits and `. _ - @`. Inline
applies to HTML bodies; with a plain-text body the file is attached normally. Mail
clients accept a name like `logo.png`; a name with `@`, such as `logo@example.com`, is
also a strict RFC 5322 message id.

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
The library never logs. Validation errors are sentinels and name the field, never its
value, so they are safe to log:

| Error | Meaning |
|---|---|
| `ErrMissingParameter` | A required field is empty |
| `ErrInvalidAddress` | A From, To, Cc or Bcc address is malformed (`"...: To[1]"`) |
| `ErrInvalidHeader` | CR or LF in Subject, Name, an address or a filename; a bad inline name or content type |
| `ErrHeaderTooLong` | A header has a word too long to fold under the 998-octet line limit |
| `ErrClosed` | `Close()` was called while the send waited for the rate limiter or a connection |
| `ErrHeaderTooLong` (attachments) | A filename, inline name or content type makes a part header over 998 octets |

Server replies arrive as `*textproto.Error` with the SMTP code. Note that a server's reply
text can quote the recipient address.

```go
err := mail.SendContext(ctx)
var reply *textproto.Error
switch {
case err == nil:
    // accepted by the server
case errors.Is(err, gomail.ErrInvalidAddress), errors.Is(err, gomail.ErrInvalidHeader),
    errors.Is(err, gomail.ErrHeaderTooLong):
    // bad input: do not retry
case errors.As(err, &reply) && reply.Code >= 500:
    // permanent rejection (e.g. 550 unknown mailbox, 554 rejected, 535 bad credentials)
case errors.Is(err, context.DeadlineExceeded):
    // timed out: the message may or may not have been accepted
default:
    // network or 4xx: retry later
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
```

`RenderTemplate` parses one file, so a template cannot include a separate layout file.
Keep each email template self-contained.

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

// 3. A server signed by a private CA
caPEM, err := os.ReadFile("internal-ca.pem")
if err != nil {
    log.Fatal(err)
}
roots := x509.NewCertPool()
roots.AppendCertsFromPEM(caPEM)
mail.SetTLSConfig(&TLSConfig{
    StartTLS: true,
    RootCAs:  roots,
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
```

The loop changes the message between sends, so it runs sequentially. To send different
messages in parallel, give each goroutine its own `Mail` (see Concurrency).

### Retries
```go
// Up to 3 attempts, waiting 2s and 4s in between; the wait honours ctx.
err := mail.SendWithRetryContext(ctx, 3)
```

Retries stop early on errors a retry cannot fix: the errors in the table above, 5xx
replies, certificate verification failures, TLS against a plain-text port, and
credentials refused before AUTH (no TLS). Seekable stream attachments (such as `*os.File`) are rewound before a retry; a
non-seekable one gets a single attempt. A network error after the message was sent may
hide an accepted message, so a retry can deliver it twice.

## Contributing
Contributions are welcome! For any feedback, bug reports, or contributions, please submit an issue or pull request to the GitHub repository.

## License
This package is licensed under the MIT License. See the LICENSE file for more information.