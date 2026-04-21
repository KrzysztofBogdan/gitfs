package jira

import (
	"bytes"
	"fmt"

	"github.com/KrzysztofBogdan/gitfs/adapter"
	"gopkg.in/yaml.v3"
)

// writeIssue emits metadata.yaml, description.md, comments.md for one issue.
func writeIssue(emit adapter.Emitter, is issue) error {
	base := is.Key

	// metadata.yaml
	meta := issueMetadata(is)
	metaYAML, err := yaml.Marshal(meta)
	if err != nil {
		return err
	}
	if err := emit.File(base+"/metadata.yaml", metaYAML); err != nil {
		return err
	}

	// description.md — prefer renderedFields.description if present.
	descHTML := renderedString(is.RenderedFields, "description")
	descMD := htmlToMarkdown(descHTML)
	if err := emit.File(base+"/description.md", []byte(descMD)); err != nil {
		return err
	}

	// comments.md
	var cb bytes.Buffer
	writeRenderedComments(&cb, is.RenderedFields)
	if err := emit.File(base+"/comments.md", cb.Bytes()); err != nil {
		return err
	}
	return nil
}

// issueMetadata flattens the commonly-referenced fields into a map for
// YAML output. Custom fields are intentionally out of scope for v1; the
// design calls them out but they require resolving the `names` expansion
// across pages, which we defer.
func issueMetadata(is issue) map[string]any {
	m := map[string]any{
		"key":       is.Key,
		"summary":   is.Fields.Summary,
		"status":    is.Fields.Status.Name,
		"issuetype": is.Fields.IssueType.Name,
		"labels":    is.Fields.Labels,
		"created":   is.Fields.Created,
		"updated":   is.Fields.Updated,
	}
	if is.Fields.Priority != nil {
		m["priority"] = is.Fields.Priority.Name
	}
	if is.Fields.Assignee != nil {
		m["assignee"] = is.Fields.Assignee.DisplayName
	}
	if is.Fields.Reporter != nil {
		m["reporter"] = is.Fields.Reporter.DisplayName
	}
	if len(is.Fields.Components) > 0 {
		names := make([]string, 0, len(is.Fields.Components))
		for _, c := range is.Fields.Components {
			names = append(names, c.Name)
		}
		m["components"] = names
	}
	if len(is.Fields.FixVersions) > 0 {
		names := make([]string, 0, len(is.Fields.FixVersions))
		for _, v := range is.Fields.FixVersions {
			names = append(names, v.Name)
		}
		m["fixVersions"] = names
	}
	if is.Fields.Parent != nil {
		m["parent"] = is.Fields.Parent.Key
	}
	return m
}

func renderedString(rf map[string]any, key string) string {
	if rf == nil {
		return ""
	}
	v, ok := rf[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func writeRenderedComments(w *bytes.Buffer, rf map[string]any) {
	if rf == nil {
		return
	}
	commentsRaw, ok := rf["comment"].(map[string]any)
	if !ok {
		return
	}
	list, ok := commentsRaw["comments"].([]any)
	if !ok {
		return
	}
	for i, c := range list {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		author := ""
		if a, ok := cm["author"].(map[string]any); ok {
			if dn, ok := a["displayName"].(string); ok {
				author = dn
			}
		}
		created, _ := cm["created"].(string)
		body, _ := cm["body"].(string)
		if i > 0 {
			w.WriteString("\n---\n\n")
		}
		fmt.Fprintf(w, "## %s — %s\n\n", author, created)
		w.WriteString(htmlToMarkdown(body))
		if len(body) > 0 && body[len(body)-1] != '\n' {
			w.WriteByte('\n')
		}
	}
}
