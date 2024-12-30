package main

import (
	"log"
	"time"

	"github.com/mstgnz/gomail"
)

func main() {
	// Create a new mail client
	mail := &gomail.Mail{
		From: "sender@example.com",
		Name: "Sender Name",
		Host: "smtp.example.com",
		Port: "587",
		User: "username",
		Pass: "password",
	}

	// Configure connection pool and timeouts
	mail.SetPoolSize(5).
		SetTimeout(10 * time.Second).
		SetKeepAlive(30 * time.Second)

	// Basic text email
	err := mail.SetSubject("Test Email").
		SetContent("This is a test email.").
		SetTo("recipient@example.com").
		Send()
	if err != nil {
		log.Fatalf("Failed to send email: %v", err)
	}

	// HTML template email
	err = mail.SetSubject("Welcome Email").
		SetTo("newuser@example.com").
		SendHtml("templates/welcome.html", map[string]any{
			"Name": "John Doe",
			"URL":  "https://example.com/activate",
		})
	if err != nil {
		log.Fatalf("Failed to send HTML email: %v", err)
	}

	// Asynchronous email with attachment
	result := mail.SetSubject("Async Email").
		SetContent("This is an async email with attachment.").
		SetTo("recipient@example.com").
		SetAttachment(map[string][]byte{
			"document.pdf": []byte("PDF content here"),
		}).
		SendAsync()

	// Check async result
	if err := <-result; err != nil {
		log.Printf("Async email failed: %v", err)
	}
}
