package gomail

import (
	"bytes"
	"html/template"
	"os"
)

// SimpleRenderTemplate renders an HTML template file with data. It uses html/template, so
// data is HTML-escaped; wrap trusted markup in template.HTML.
func SimpleRenderTemplate(filePath string, data map[string]any) (string, error) {
	fileContent, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	tmpl, err := template.New("email").Parse(string(fileContent))
	if err != nil {
		return "", err
	}

	var renderedContent bytes.Buffer
	if err := tmpl.Execute(&renderedContent, data); err != nil {
		return "", err
	}

	return renderedContent.String(), nil
}
