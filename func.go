package gomail

import (
	"bytes"
	"os"
	"text/template"
)

// SimpleRenderTemplate renders a simple HTML template with dynamic data
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
