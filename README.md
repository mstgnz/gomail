# gomail
This is a Go package for sending emails. It provides functionality to send emails using the SMTP protocol.


## Installation
To use the package, you need to have Go installed and Go modules enabled in your project. Then you can add the package to your project by running the following command:

```bash
go get -u github.com/mstgnz/gomail
```


## Usage
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

    // Send the email with text
    err := mail.SetSubject("Test Email").
        SetContent("This is a test email.").
        SetTo([]string{"recipient@example.com"}).
        Send()
    if err != nil {
        // Handle error
    }

    // Send the email with HTML
    err = mail.SetSubject("Test Email").
        SetTo("recipient@example.com").
        SendHtml("view/forgot-password.html", map[string]any{
			"Code": code,
			"Link": link,
		})
    if err != nil {
        // Handle error
    }

    // Send the email with attachment
    attachments := map[string][]byte{
        "file.txt": []byte("Attachment content"),
    }
    err = mail.SetSubject("Test Email with Attachment").
        SetContent("This is a test email with attachment.").
        SetTo("recipient@example.com").
        SetAttachment(attachments).
        Send()
    if err != nil {
        // Handle error
    }
}
```


## Features
- Uses the SMTP protocol for sending emails.
- Provides error handling mechanism during email sending process.
- Allows setting basic parameters required for email sending.


## Contributing
Contributions are welcome! For any feedback, bug reports, or contributions, please submit an issue or pull request to the GitHub repository.


## License
This package is licensed under the MIT License. See the LICENSE file for more information.