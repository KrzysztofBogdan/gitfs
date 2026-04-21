package jira

import (
	htm "github.com/JohannesKaufmann/html-to-markdown/v2"
)

// htmlToMarkdown converts HTML (from Jira's renderedFields) to markdown.
// Failure falls back to the raw input — the field may already be plain.
func htmlToMarkdown(html string) string {
	if html == "" {
		return ""
	}
	md, err := htm.ConvertString(html)
	if err != nil {
		return html
	}
	return md
}
