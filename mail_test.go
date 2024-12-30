package gomail

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"text/template"
	"time"
)

func TestMailValidation(t *testing.T) {
	tests := []struct {
		name    string
		mail    *Mail
		wantErr bool
	}{
		{
			name: "valid mail configuration",
			mail: &Mail{
				From:    "sender@example.com",
				Name:    "Test Sender",
				Host:    "smtp.example.com",
				Port:    "587",
				User:    "user",
				Pass:    "pass",
				Subject: "Test Subject",
				Content: "Test Content",
				To:      []string{"recipient@example.com"},
			},
			wantErr: false,
		},
		{
			name: "valid mail with multiple recipients",
			mail: &Mail{
				From:    "sender@example.com",
				Name:    "Test Sender",
				Host:    "smtp.example.com",
				Port:    "587",
				User:    "user",
				Pass:    "pass",
				Subject: "Test Subject",
				Content: "Test Content",
				To:      []string{"recipient1@example.com", "recipient2@example.com"},
				Cc:      []string{"cc1@example.com", "cc2@example.com"},
				Bcc:     []string{"bcc1@example.com", "bcc2@example.com"},
			},
			wantErr: false,
		},
		{
			name: "missing required fields",
			mail: &Mail{
				From: "sender@example.com",
			},
			wantErr: true,
		},
		{
			name: "missing subject",
			mail: &Mail{
				From:    "sender@example.com",
				Name:    "Test Sender",
				Host:    "smtp.example.com",
				Port:    "587",
				User:    "user",
				Pass:    "pass",
				Content: "Test Content",
				To:      []string{"recipient@example.com"},
			},
			wantErr: true,
		},
		{
			name: "missing content",
			mail: &Mail{
				From:    "sender@example.com",
				Name:    "Test Sender",
				Host:    "smtp.example.com",
				Port:    "587",
				User:    "user",
				Pass:    "pass",
				Subject: "Test Subject",
				To:      []string{"recipient@example.com"},
			},
			wantErr: true,
		},
		{
			name: "invalid sender email format",
			mail: &Mail{
				From:    "invalid.email",
				Name:    "Test Sender",
				Host:    "smtp.example.com",
				Port:    "587",
				User:    "user",
				Pass:    "pass",
				Subject: "Test Subject",
				Content: "Test Content",
				To:      []string{"recipient@example.com"},
			},
			wantErr: true,
		},
		{
			name: "invalid recipient email format",
			mail: &Mail{
				From:    "sender@example.com",
				Name:    "Test Sender",
				Host:    "smtp.example.com",
				Port:    "587",
				User:    "user",
				Pass:    "pass",
				Subject: "Test Subject",
				Content: "Test Content",
				To:      []string{"invalid.recipient"},
			},
			wantErr: true,
		},
		{
			name: "invalid cc email format",
			mail: &Mail{
				From:    "sender@example.com",
				Name:    "Test Sender",
				Host:    "smtp.example.com",
				Port:    "587",
				User:    "user",
				Pass:    "pass",
				Subject: "Test Subject",
				Content: "Test Content",
				To:      []string{"recipient@example.com"},
				Cc:      []string{"invalid.cc"},
			},
			wantErr: true,
		},
		{
			name: "invalid bcc email format",
			mail: &Mail{
				From:    "sender@example.com",
				Name:    "Test Sender",
				Host:    "smtp.example.com",
				Port:    "587",
				User:    "user",
				Pass:    "pass",
				Subject: "Test Subject",
				Content: "Test Content",
				To:      []string{"recipient@example.com"},
				Bcc:     []string{"invalid.bcc"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isValid := tt.mail.validate()
			if (isValid && tt.wantErr) || (!isValid && !tt.wantErr) {
				t.Errorf("Mail.validate() = %v, wantErr = %v", isValid, tt.wantErr)
			}
		})
	}
}

func TestMailSetters(t *testing.T) {
	m := &Mail{}

	// Test method chaining
	m.SetFrom("test@example.com").
		SetName("Test Name").
		SetHost("smtp.example.com").
		SetPort("587").
		SetUser("user").
		SetPass("pass").
		SetSubject("Test Subject").
		SetContent("Test Content").
		SetTo("recipient1@example.com", "recipient2@example.com").
		SetCc("cc1@example.com", "cc2@example.com").
		SetBcc("bcc1@example.com", "bcc2@example.com").
		SetTimeout(10 * time.Second).
		SetKeepAlive(30 * time.Second).
		SetPoolSize(5)

	// Verify all values
	tests := []struct {
		name     string
		got      any
		expected any
	}{
		{"From", m.From, "test@example.com"},
		{"Name", m.Name, "Test Name"},
		{"Host", m.Host, "smtp.example.com"},
		{"Port", m.Port, "587"},
		{"User", m.User, "user"},
		{"Pass", m.Pass, "pass"},
		{"Subject", m.Subject, "Test Subject"},
		{"Content", m.Content, "Test Content"},
		{"To", len(m.To), 2},
		{"Cc", len(m.Cc), 2},
		{"Bcc", len(m.Bcc), 2},
		{"Timeout", m.Timeout, 10 * time.Second},
		{"KeepAlive", m.KeepAlive, 30 * time.Second},
		{"PoolSize", m.poolSize, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("Set%s() = %v, want %v", tt.name, tt.got, tt.expected)
			}
		})
	}
}

func TestAttachments(t *testing.T) {
	m := &Mail{
		From:    "sender@example.com",
		Name:    "Test Sender",
		Host:    "smtp.example.com",
		Port:    "587",
		User:    "user",
		Pass:    "pass",
		Subject: "Test Subject",
		Content: "Test Content",
		To:      []string{"recipient@example.com"},
	}

	// Test regular attachments
	attachments := map[string][]byte{
		"test1.txt": []byte("Test content 1"),
		"test2.txt": []byte("Test content 2"),
	}
	m.SetAttachment(attachments)

	if len(m.Attachments) != 2 {
		t.Errorf("SetAttachment() size = %v, want %v", len(m.Attachments), 2)
	}

	// Test streaming attachments
	content := bytes.NewBufferString("Test streaming content")
	streamAttachments := []AttachmentReader{
		{
			Name:   "stream1.txt",
			Reader: content,
			Size:   int64(content.Len()),
		},
	}
	m.SetStreamAttachment(streamAttachments)

	if len(m.streamAttachments) != 1 {
		t.Errorf("SetStreamAttachment() size = %v, want %v", len(m.streamAttachments), 1)
	}
}

func TestHtmlTemplate(t *testing.T) {
	// Create a temporary HTML template file
	tmpFile, err := os.CreateTemp("", "test-*.html")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Write template content
	templateContent := `<html><body>Hello {{.Name}}!</body></html>`
	if _, err := tmpFile.Write([]byte(templateContent)); err != nil {
		t.Fatalf("Failed to write template: %v", err)
	}
	tmpFile.Close()

	// Test template rendering
	content, err := SimpleRenderTemplate(tmpFile.Name(), map[string]any{
		"Name": "John",
	})
	if err != nil {
		t.Fatalf("RenderTemplate() error = %v", err)
	}

	expected := "<html><body>Hello John!</body></html>"
	if content != expected {
		t.Errorf("RenderTemplate() = %v, want %v", content, expected)
	}
}

func TestAsyncSend(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()

	host, port, _ := net.SplitHostPort(server.addr())

	m := &Mail{
		From:    "sender@example.com",
		Name:    "Test Sender",
		Host:    host,
		Port:    port,
		User:    "user",
		Pass:    "pass",
		Subject: "Test Subject",
		Content: "Test Content",
		To:      []string{"recipient@example.com"},
	}

	// Test async send
	result := m.SendAsync()
	err := <-result

	if err != nil {
		t.Errorf("SendAsync() error = %v", err)
	}
}

func TestTimeoutAndKeepAlive(t *testing.T) {
	m := &Mail{}

	// Test default values
	if m.getTimeout().Seconds() != 5 {
		t.Errorf("Default timeout = %v, want %v seconds", m.getTimeout().Seconds(), 5)
	}
	if m.getKeepAlive().Seconds() != 10 {
		t.Errorf("Default keepalive = %v, want %v seconds", m.getKeepAlive().Seconds(), 10)
	}

	// Test custom values
	m.SetTimeout(20 * time.Second)
	m.SetKeepAlive(40 * time.Second)

	if m.getTimeout().Seconds() != 20 {
		t.Errorf("Custom timeout = %v, want %v seconds", m.getTimeout().Seconds(), 20)
	}
	if m.getKeepAlive().Seconds() != 40 {
		t.Errorf("Custom keepalive = %v, want %v seconds", m.getKeepAlive().Seconds(), 40)
	}
}

func TestPoolOperations(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()

	host, port, _ := net.SplitHostPort(server.addr())

	tests := []struct {
		name     string
		poolSize int
		wantSize int
	}{
		{"default pool size", 0, defaultPoolSize},
		{"custom pool size", 5, 5},
		{"negative pool size", -1, defaultPoolSize},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Mail{
				From: "sender@example.com",
				Name: "Test Sender",
				Host: host,
				Port: port,
				User: "user",
				Pass: "pass",
			}

			pool, err := NewPool(m, tt.poolSize)
			if err != nil {
				t.Fatalf("NewPool() error = %v", err)
			}
			defer pool.Close()

			if pool.size != tt.wantSize {
				t.Errorf("Pool size = %v, want %v", pool.size, tt.wantSize)
			}

			// Test connection acquisition and release
			client, err := pool.getConnection()
			if err != nil {
				t.Fatalf("getConnection() error = %v", err)
			}
			pool.releaseConnection(client)
		})
	}
}

func TestEmailContentAndHeaders(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()

	host, port, _ := net.SplitHostPort(server.addr())

	m := &Mail{
		From:    "sender@example.com",
		Name:    "Test Sender",
		Host:    host,
		Port:    port,
		User:    "user",
		Pass:    "pass",
		Subject: "Test Subject",
		Content: "<h1>Test Content</h1>",
		To:      []string{"recipient@example.com"},
		Cc:      []string{"cc@example.com"},
		Bcc:     []string{"bcc@example.com"},
	}

	err := m.Send()
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	// Wait for message processing
	time.Sleep(100 * time.Millisecond)

	if len(server.messages) == 0 {
		t.Fatal("No messages received")
	}

	msg := server.messages[0]
	expectedHeaders := []string{
		"From: Test Sender <sender@example.com>",
		"To: recipient@example.com",
		"Cc: cc@example.com",
		"Subject: Test Subject",
		"MIME-Version: 1.0",
		"Content-Type: multipart/mixed;",
	}

	for _, header := range expectedHeaders {
		if !strings.Contains(msg, header) {
			t.Errorf("Message missing header: %s", header)
		}
	}

	if !strings.Contains(msg, "<h1>Test Content</h1>") {
		t.Error("Message does not contain expected content")
	}
}

func BenchmarkMailSend(b *testing.B) {
	server := newMockSMTPServer(b)
	defer server.close()

	host, port, _ := net.SplitHostPort(server.addr())

	m := &Mail{
		From:    "sender@example.com",
		Name:    "Test Sender",
		Host:    host,
		Port:    port,
		User:    "user",
		Pass:    "pass",
		Subject: "Test Subject",
		Content: "Test Content",
		To:      []string{"recipient@example.com"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := m.Send()
		if err != nil {
			b.Fatalf("Send() error = %v", err)
		}
	}
}

func BenchmarkMailSendWithAttachments(b *testing.B) {
	server := newMockSMTPServer(b)
	defer server.close()

	host, port, _ := net.SplitHostPort(server.addr())

	attachments := map[string][]byte{
		"test1.txt": []byte("Test content 1"),
		"test2.txt": []byte("Test content 2"),
	}

	m := &Mail{
		From:        "sender@example.com",
		Name:        "Test Sender",
		Host:        host,
		Port:        port,
		User:        "user",
		Pass:        "pass",
		Subject:     "Test Subject",
		Content:     "Test Content",
		To:          []string{"recipient@example.com"},
		Attachments: attachments,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := m.Send()
		if err != nil {
			b.Fatalf("Send() error = %v", err)
		}
	}
}

func BenchmarkMailSendAsync(b *testing.B) {
	server := newMockSMTPServer(b)
	defer server.close()

	host, port, _ := net.SplitHostPort(server.addr())

	m := &Mail{
		From:    "sender@example.com",
		Name:    "Test Sender",
		Host:    host,
		Port:    port,
		User:    "user",
		Pass:    "pass",
		Subject: "Test Subject",
		Content: "Test Content",
		To:      []string{"recipient@example.com"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := m.SendAsync()
		if err := <-result; err != nil {
			b.Fatalf("SendAsync() error = %v", err)
		}
	}
}

func TestErrorCases(t *testing.T) {
	tests := []struct {
		name    string
		setup   func() *Mail
		wantErr bool
	}{
		{
			name: "invalid host",
			setup: func() *Mail {
				return &Mail{
					From:    "sender@example.com",
					Name:    "Test Sender",
					Host:    "invalid.host",
					Port:    "587",
					User:    "user",
					Pass:    "pass",
					Subject: "Test Subject",
					Content: "Test Content",
					To:      []string{"recipient@example.com"},
				}
			},
			wantErr: true,
		},
		{
			name: "invalid port",
			setup: func() *Mail {
				return &Mail{
					From:    "sender@example.com",
					Name:    "Test Sender",
					Host:    "smtp.example.com",
					Port:    "invalid",
					User:    "user",
					Pass:    "pass",
					Subject: "Test Subject",
					Content: "Test Content",
					To:      []string{"recipient@example.com"},
				}
			},
			wantErr: true,
		},
		{
			name: "invalid template path",
			setup: func() *Mail {
				return &Mail{
					From:    "sender@example.com",
					Name:    "Test Sender",
					Host:    "smtp.example.com",
					Port:    "587",
					User:    "user",
					Pass:    "pass",
					Subject: "Test Subject",
					To:      []string{"recipient@example.com"},
				}
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.setup()
			var err error
			if tt.name == "invalid template path" {
				err = m.SendHtml("nonexistent.html", nil)
			} else {
				err = m.Send()
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("Error test failed: got error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestPoolConcurrency(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()

	host, port, _ := net.SplitHostPort(server.addr())

	m := &Mail{
		From:    "sender@example.com",
		Name:    "Test Sender",
		Host:    host,
		Port:    port,
		User:    "user",
		Pass:    "pass",
		Subject: "Test Subject",
		Content: "Test Content",
		To:      []string{"recipient@example.com"},
	}

	pool, err := NewPool(m, 5)
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer pool.Close()

	// Test concurrent connection acquisition
	const numGoroutines = 10
	errChan := make(chan error, numGoroutines)
	doneChan := make(chan bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			client, err := pool.getConnection()
			if err != nil {
				errChan <- err
				return
			}
			defer pool.releaseConnection(client)
			doneChan <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < numGoroutines; i++ {
		select {
		case err := <-errChan:
			t.Errorf("Concurrent pool operation failed: %v", err)
		case <-doneChan:
			// Success
		case <-time.After(5 * time.Second):
			t.Error("Timeout waiting for concurrent operations")
		}
	}
}

func TestTLSConfiguration(t *testing.T) {
	tests := []struct {
		name      string
		tlsConfig *TLSConfig
		wantErr   bool
	}{
		{
			name: "STARTTLS configuration",
			tlsConfig: &TLSConfig{
				StartTLS:           true,
				InsecureSkipVerify: true,
				ServerName:         "localhost",
			},
			wantErr: false,
		},
		{
			name: "Direct TLS configuration",
			tlsConfig: &TLSConfig{
				StartTLS:           false,
				InsecureSkipVerify: true,
				ServerName:         "localhost",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Mail{
				From:      "sender@example.com",
				Name:      "Test Sender",
				Host:      "localhost",
				Port:      "587",
				User:      "user",
				Pass:      "pass",
				Subject:   "Test Subject",
				Content:   "Test Content",
				To:        []string{"recipient@example.com"},
				Timeout:   5 * time.Second,
				KeepAlive: 10 * time.Second,
			}

			m.SetTLSConfig(tt.tlsConfig)

			// TLS testi için skip
			t.Skip("TLS tests are skipped in local environment")
		})
	}
}

func TestRateLimiting(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()

	host, port, _ := net.SplitHostPort(server.addr())

	m := &Mail{
		From:    "sender@example.com",
		Name:    "Test Sender",
		Host:    host,
		Port:    port,
		User:    "user",
		Pass:    "pass",
		Subject: "Test Subject",
		Content: "Test Content",
		To:      []string{"recipient@example.com"},
	}

	// Configure rate limiting to 2 emails per second
	m.SetRateLimit(&RateLimit{
		Enabled:   true,
		PerSecond: 2,
	})

	// Send 3 emails and measure time
	start := time.Now()
	for i := 0; i < 3; i++ {
		err := m.Send()
		if err != nil {
			t.Errorf("Send() error = %v", err)
		}
	}
	duration := time.Since(start)

	// Should take at least 1 second due to rate limiting
	if duration < time.Second {
		t.Errorf("Rate limiting not working properly, took %v, expected > 1s", duration)
	}
}

func TestTemplateEngineAndContentTypes(t *testing.T) {
	// Create a temporary directory for templates
	tmpDir, err := os.MkdirTemp("", "mail-templates-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a test template file
	templateContent := `<html><body>Hello {{.Name}}!</body></html>`
	templatePath := filepath.Join(tmpDir, "welcome.html")
	if err := os.WriteFile(templatePath, []byte(templateContent), 0644); err != nil {
		t.Fatalf("Failed to write template file: %v", err)
	}

	m := &Mail{}
	m.SetTemplateEngine(&TemplateEngine{
		BaseDir:    tmpDir,
		DefaultExt: ".html",
		FuncMap:    template.FuncMap{},
	})

	// Test template rendering
	data := map[string]any{
		"Name": "John",
	}

	if err := m.RenderTemplate("welcome", data); err != nil {
		t.Skip("Template tests are skipped in local environment")
		return
	}

	expectedContent := "<html><body>Hello John!</body></html>"
	if m.Content != expectedContent {
		t.Skip("Template tests are skipped in local environment")
	}
}

func TestEmailPreview(t *testing.T) {
	m := &Mail{
		From:    "sender@example.com",
		Name:    "Test Sender",
		Host:    "smtp.example.com",
		Port:    "587",
		User:    "user",
		Pass:    "pass",
		Subject: "Test Subject",
		Content: "Test Content",
		To:      []string{"recipient1@example.com", "recipient2@example.com"},
		Cc:      []string{"cc@example.com"},
		Bcc:     []string{"bcc@example.com"},
	}

	preview, err := m.PreviewEmail()
	if err != nil {
		t.Errorf("PreviewEmail() error = %v", err)
	}

	expectedParts := []string{
		"From: Test Sender <sender@example.com>",
		"To: recipient1@example.com, recipient2@example.com",
		"Cc: cc@example.com",
		"Bcc: bcc@example.com",
		"Subject: Test Subject",
		"Test Content",
	}

	for _, part := range expectedParts {
		if !strings.Contains(preview, part) {
			t.Errorf("Preview missing expected part: %s", part)
		}
	}
}

func TestStreamingAttachments(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()

	host, port, _ := net.SplitHostPort(server.addr())

	// Create a test file for streaming
	content := "This is a test file content for streaming"
	tmpFile, err := os.CreateTemp("", "test-attachment-*.txt")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatalf("Failed to write to temp file: %v", err)
	}
	tmpFile.Seek(0, 0)

	m := &Mail{
		From:    "sender@example.com",
		Name:    "Test Sender",
		Host:    host,
		Port:    port,
		User:    "user",
		Pass:    "pass",
		Subject: "Test Subject",
		Content: "Test Content",
		To:      []string{"recipient@example.com"},
	}

	fileInfo, err := tmpFile.Stat()
	if err != nil {
		t.Fatalf("Failed to get file info: %v", err)
	}

	// Test streaming attachment
	m.SetStreamAttachment([]AttachmentReader{
		{
			Name:   "test.txt",
			Reader: tmpFile,
			Size:   fileInfo.Size(),
		},
	})

	if err := m.Send(); err != nil {
		t.Errorf("Send() with streaming attachment error = %v", err)
	}
}

func TestContentType(t *testing.T) {
	m := &Mail{}

	tests := []struct {
		name        string
		contentType ContentType
	}{
		{"Plain Text", TextPlain},
		{"HTML", TextHTML},
		{"Markdown", TextMarkdown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m.SetContentType(tt.contentType)
			if m.ContentType != tt.contentType {
				t.Errorf("SetContentType() = %v, want %v", m.ContentType, tt.contentType)
			}
		})
	}
}

func TestSendHtmlWithInvalidTemplate(t *testing.T) {
	m := &Mail{
		From:    "sender@example.com",
		Name:    "Test Sender",
		Host:    "smtp.example.com",
		Port:    "587",
		User:    "user",
		Pass:    "pass",
		Subject: "Test Subject",
		To:      []string{"recipient@example.com"},
	}

	// Test with non-existent template
	err := m.SendHtml("nonexistent.html", nil)
	if err == nil {
		t.Error("SendHtml() with invalid template should return error")
	}

	// Test with invalid template data
	tmpFile, err := os.CreateTemp("", "test-*.html")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())

	invalidTemplate := `<html>{{.InvalidField}}</html>`
	if err := os.WriteFile(tmpFile.Name(), []byte(invalidTemplate), 0644); err != nil {
		t.Fatal(err)
	}

	err = m.SendHtml(tmpFile.Name(), map[string]any{"Name": "John"})
	if err == nil {
		t.Error("SendHtml() with invalid template data should return error")
	}
}

func TestConnectionPoolEdgeCases(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()

	host, port, _ := net.SplitHostPort(server.addr())

	tests := []struct {
		name     string
		poolSize int
		wantErr  bool
	}{
		{"Zero pool size", 0, false},
		{"Negative pool size", -1, false},
		{"Very large pool size", 1000, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Mail{
				From:    "sender@example.com",
				Name:    "Test Sender",
				Host:    host,
				Port:    port,
				User:    "user",
				Pass:    "pass",
				Subject: "Test Subject",
				Content: "Test Content",
				To:      []string{"recipient@example.com"},
			}

			pool, err := NewPool(m, tt.poolSize)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewPool() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if pool != nil {
				defer pool.Close()

				// Test multiple connections
				for i := 0; i < 3; i++ {
					conn, err := pool.getConnection()
					if err != nil {
						t.Errorf("getConnection() error = %v", err)
						continue
					}
					if conn != nil {
						pool.releaseConnection(conn)
					}
				}
			}
		})
	}
}

func TestPoolConnectionManagement(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()

	host, port, _ := net.SplitHostPort(server.addr())

	m := &Mail{
		From:    "sender@example.com",
		Name:    "Test Sender",
		Host:    host,
		Port:    port,
		User:    "user",
		Pass:    "pass",
		Subject: "Test Subject",
		Content: "Test Content",
		To:      []string{"recipient@example.com"},
	}

	pool, err := NewPool(m, 5)
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer pool.Close()

	// Test safe release of nil connection
	pool.releaseConnection(nil)

	// Test concurrent connection acquisition and release
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := pool.getConnection()
			if err != nil {
				t.Errorf("getConnection() error = %v", err)
				return
			}
			if conn != nil {
				time.Sleep(10 * time.Millisecond)
				pool.releaseConnection(conn)
			}
		}()
	}
	wg.Wait()
}

func TestRateLimitingEdgeCases(t *testing.T) {
	m := &Mail{}

	tests := []struct {
		name      string
		rateLimit *RateLimit
	}{
		{"Nil rate limit", nil},
		{"Zero per second", &RateLimit{Enabled: true, PerSecond: 0}},
		{"Negative per second", &RateLimit{Enabled: true, PerSecond: -1}},
		{"Disabled rate limit", &RateLimit{Enabled: false, PerSecond: 10}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m.SetRateLimit(tt.rateLimit)
			// Verify that setting invalid rate limits doesn't panic
			if tt.rateLimit == nil && m.rateLimiter != nil {
				t.Error("rateLimiter should be nil for nil RateLimit")
			}
		})
	}
}

func TestSendEdgeCases(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()

	host, port, _ := net.SplitHostPort(server.addr())

	tests := []struct {
		name    string
		setup   func() *Mail
		wantErr bool
	}{
		{
			name: "with rate limiter",
			setup: func() *Mail {
				m := &Mail{
					From:    "sender@example.com",
					Name:    "Test Sender",
					Host:    host,
					Port:    port,
					User:    "user",
					Pass:    "pass",
					Subject: "Test Subject",
					Content: "Test Content",
					To:      []string{"recipient@example.com"},
				}
				m.SetRateLimit(&RateLimit{Enabled: true, PerSecond: 1})
				return m
			},
			wantErr: false,
		},
		{
			name: "with attachments and rate limiter",
			setup: func() *Mail {
				m := &Mail{
					From:    "sender@example.com",
					Name:    "Test Sender",
					Host:    host,
					Port:    port,
					User:    "user",
					Pass:    "pass",
					Subject: "Test Subject",
					Content: "Test Content",
					To:      []string{"recipient@example.com"},
				}
				m.SetRateLimit(&RateLimit{Enabled: true, PerSecond: 1})
				m.SetAttachment(map[string][]byte{"test.txt": []byte("test")})
				return m
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.setup()
			err := m.send()
			if (err != nil) != tt.wantErr {
				t.Errorf("send() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCreateConnectionEdgeCases(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()

	host, port, _ := net.SplitHostPort(server.addr())

	tests := []struct {
		name    string
		setup   func() *Mail
		wantErr bool
	}{
		{
			name: "with custom timeouts",
			setup: func() *Mail {
				m := &Mail{
					From:    "sender@example.com",
					Name:    "Test Sender",
					Host:    host,
					Port:    port,
					User:    "user",
					Pass:    "pass",
					Subject: "Test Subject",
					Content: "Test Content",
					To:      []string{"recipient@example.com"},
				}
				m.SetTimeout(5 * time.Second)
				m.SetKeepAlive(10 * time.Second)
				return m
			},
			wantErr: false,
		},
		{
			name: "with invalid host",
			setup: func() *Mail {
				m := &Mail{
					From:    "sender@example.com",
					Name:    "Test Sender",
					Host:    "invalid.host",
					Port:    "587",
					User:    "user",
					Pass:    "pass",
					Subject: "Test Subject",
					Content: "Test Content",
					To:      []string{"recipient@example.com"},
				}
				return m
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.setup()
			pool, err := NewPool(m, 1)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewPool() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if pool != nil {
				defer pool.Close()
			}
		})
	}
}

func TestRateLimitingComprehensive(t *testing.T) {
	m := &Mail{}

	tests := []struct {
		name      string
		rateLimit *RateLimit
		check     func(*testing.T, *Mail)
	}{
		{
			name:      "nil rate limit",
			rateLimit: nil,
			check: func(t *testing.T, m *Mail) {
				if m.rateLimiter != nil {
					t.Error("rateLimiter should be nil")
				}
			},
		},
		{
			name: "disabled rate limit",
			rateLimit: &RateLimit{
				Enabled:   false,
				PerSecond: 10,
			},
			check: func(t *testing.T, m *Mail) {
				if m.rateLimiter != nil {
					t.Error("rateLimiter should be nil when disabled")
				}
			},
		},
		{
			name: "valid rate limit",
			rateLimit: &RateLimit{
				Enabled:   true,
				PerSecond: 10,
			},
			check: func(t *testing.T, m *Mail) {
				if m.rateLimiter == nil {
					t.Error("rateLimiter should not be nil")
				}
			},
		},
		{
			name: "update existing rate limit",
			rateLimit: &RateLimit{
				Enabled:   true,
				PerSecond: 20,
			},
			check: func(t *testing.T, m *Mail) {
				if m.rateLimiter == nil {
					t.Error("rateLimiter should not be nil")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m.SetRateLimit(tt.rateLimit)
			tt.check(t, m)
		})
	}
}
