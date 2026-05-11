package us

import (
	"os"
	"strings"
)

func getenvDefault(key, fallback string) string {
	if value := strings.TrimSpace(getenv(key)); value != "" {
		return value
	}
	return fallback
}

func responsePreview(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if len(text) <= 400 {
		return text
	}
	return text[:400] + "..."
}

var getenv = os.Getenv
