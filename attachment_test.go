package gomail

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"testing"
)

// topLevelTypes returns the Content-Type of each top-level part of a raw message.
func topLevelTypes(t *testing.T, raw string) []string {
	t.Helper()
	msg := parseMessage(t, raw)
	_, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	var types []string
	for {
		p, err := mr.NextRawPart()
		if err == io.EOF {
			return types
		}
		if err != nil {
			t.Fatal(err)
		}
		types = append(types, p.Header.Get("Content-Type"))
	}
}

func TestInlineAndTypedAttachments(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	logo := []byte("\x89PNG\r\n\x1a\nfake image bytes")
	ics := []byte("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n")
	m.SetContent(`<p>Rezervasyon onaylandı</p><img src="cid:logo.png">`).
		SetPlainText("Rezervasyon onaylandı").
		SetAttachments([]Attachment{
			{Name: "logo.png", Data: logo, Inline: true},
			{Name: "bilet.pdf", Data: []byte("%PDF-1.4")},
			{Name: "event.ics", ContentType: "text/calendar; method=REQUEST", Data: ics},
		})

	if err := m.Send(); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	raw := onlyMessage(t, server)

	types := topLevelTypes(t, raw)
	if len(types) != 3 {
		t.Fatalf("top-level parts = %v, want related + 2 attachments", types)
	}
	mediaType, params, err := mime.ParseMediaType(types[0])
	if err != nil || mediaType != "multipart/related" || params["type"] != "multipart/alternative" {
		t.Errorf("first part = %q, want multipart/related; type=multipart/alternative", types[0])
	}

	byName := map[string]mimePart{}
	for _, p := range messageParts(t, raw) {
		if p.disposition == "" {
			continue
		}
		_, dp, err := mime.ParseMediaType(p.disposition)
		if err != nil {
			t.Fatal(err)
		}
		byName[dp["filename"]] = p
	}

	inline := byName["logo.png"]
	if !strings.HasPrefix(inline.disposition, "inline") || inline.contentID != "<logo.png>" || inline.contentType != "image/png" {
		t.Errorf("inline part = %q / %q / %q", inline.disposition, inline.contentID, inline.contentType)
	}
	if string(inline.body) != string(logo) {
		t.Error("inline image changed in transfer")
	}
	pdf := byName["bilet.pdf"]
	if !strings.HasPrefix(pdf.disposition, "attachment") || pdf.contentType != "application/pdf" || pdf.contentID != "" {
		t.Errorf("pdf part = %q / %q / %q", pdf.disposition, pdf.contentType, pdf.contentID)
	}
	if cal := byName["event.ics"]; cal.contentType != "text/calendar; method=REQUEST" || string(cal.body) != string(ics) {
		t.Errorf("calendar part = %q %q", cal.contentType, cal.body)
	}
}

func TestInlineAttachmentWithPlainBodyIsAttached(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()
	m := newTestMail(t, server)
	m.SetContentType(TextPlain).
		SetAttachments([]Attachment{{Name: "logo with space.png", Data: []byte("x"), Inline: true}})

	if err := m.Send(); err != nil {
		t.Fatalf("Send() error = %v; Inline is ignored for a plain body", err)
	}
	raw := onlyMessage(t, server)
	if strings.Contains(raw, "multipart/related") || strings.Contains(raw, "Content-Id") || strings.Contains(raw, "Content-ID") {
		t.Error("a plain body got a related part or a Content-ID")
	}
	if types := topLevelTypes(t, raw); len(types) != 2 {
		t.Errorf("top-level parts = %v, want body and attachment", types)
	}
}

func TestInvalidAttachmentsAreRefused(t *testing.T) {
	server := newMockSMTPServer(t)
	defer server.close()

	cases := map[string][]Attachment{
		"inline name with a space":  {{Name: "my logo.png", Inline: true}},
		"inline name with brackets": {{Name: "logo>.png", Inline: true}},
		"duplicate inline names":    {{Name: "a.png", Inline: true}, {Name: "a.png", Inline: true}},
		"malformed content type":    {{Name: "a.bin", ContentType: "not a type"}},
		"CR in content type":        {{Name: "a.bin", ContentType: "text/plain\r\nX-Evil: 1"}},
		"LF in name":                {{Name: "a.pdf\nX-Evil: 1"}},
	}
	for name, attachments := range cases {
		t.Run(name, func(t *testing.T) {
			m := newTestMail(t, server)
			m.SetAttachments(attachments)
			if err := m.Send(); !errors.Is(err, ErrInvalidHeader) {
				t.Errorf("Send() error = %v, want ErrInvalidHeader", err)
			}
		})
	}
	if n := server.connCount(); n != 0 {
		t.Errorf("connections = %d; a refused message must not reach the network", n)
	}
}

func TestAttachmentContentType(t *testing.T) {
	cases := []struct {
		a    Attachment
		want string
	}{
		{Attachment{Name: "a.png"}, "image/png"},
		{Attachment{Name: "a.pdf"}, "application/pdf"},
		{Attachment{Name: "noext"}, "application/octet-stream"},
		{Attachment{Name: "a.png", ContentType: "image/x-custom"}, "image/x-custom"},
	}
	for _, c := range cases {
		if got := c.a.contentType(); got != c.want {
			t.Errorf("contentType(%q, %q) = %q, want %q", c.a.Name, c.a.ContentType, got, c.want)
		}
	}
}
