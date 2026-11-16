package jira

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// adfNode is a node of Atlassian Document Format, the rich-text JSON Jira Cloud
// uses for descriptions and long text fields.
type adfNode struct {
	Type    string                     `json:"type"`
	Text    string                     `json:"text"`
	Attrs   map[string]json.RawMessage `json:"attrs"`
	Content []adfNode                  `json:"content"`
}

// adfToText flattens a description to readable plain text. Older sites and
// some custom fields return a bare string instead of a document; both work.
// The result is what an agent reads, so lists, headings, code and tables keep a
// simple Markdown-like shape.
func adfToText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return strings.TrimSpace(plain)
	}
	var doc adfNode
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	var b strings.Builder
	writeBlocks(&b, doc.Content, "")
	return strings.TrimSpace(collapseBlankLines(b.String()))
}

func writeBlocks(b *strings.Builder, nodes []adfNode, indent string) {
	for _, n := range nodes {
		writeBlock(b, n, indent)
	}
}

func writeBlock(b *strings.Builder, n adfNode, indent string) {
	switch n.Type {
	case "paragraph":
		b.WriteString(indent + inlineText(n.Content) + "\n\n")
	case "heading":
		level := 1
		if raw, ok := n.Attrs["level"]; ok {
			_ = json.Unmarshal(raw, &level)
		}
		if level < 1 || level > 6 {
			level = 1
		}
		b.WriteString(indent + strings.Repeat("#", level) + " " + inlineText(n.Content) + "\n\n")
	case "bulletList", "orderedList":
		for i, item := range n.Content {
			marker := "- "
			if n.Type == "orderedList" {
				marker = strconv.Itoa(i+1) + ". "
			}
			writeListItem(b, item, indent, marker)
		}
		b.WriteString("\n")
	case "taskList":
		for _, item := range n.Content {
			marker := "- [ ] "
			if raw, ok := item.Attrs["state"]; ok && strings.Contains(string(raw), "DONE") {
				marker = "- [x] "
			}
			writeListItem(b, item, indent, marker)
		}
		b.WriteString("\n")
	case "codeBlock":
		b.WriteString(indent + "```\n" + inlineText(n.Content) + "\n" + indent + "```\n\n")
	case "blockquote":
		var inner strings.Builder
		writeBlocks(&inner, n.Content, "")
		for _, line := range strings.Split(strings.TrimSpace(inner.String()), "\n") {
			b.WriteString(indent + "> " + line + "\n")
		}
		b.WriteString("\n")
	case "rule":
		b.WriteString(indent + "---\n\n")
	case "table":
		for _, row := range n.Content {
			cells := make([]string, 0, len(row.Content))
			for _, cell := range row.Content {
				var inner strings.Builder
				writeBlocks(&inner, cell.Content, "")
				cells = append(cells, strings.Join(strings.Fields(inner.String()), " "))
			}
			b.WriteString(indent + "| " + strings.Join(cells, " | ") + " |\n")
		}
		b.WriteString("\n")
	case "panel", "expand", "nestedExpand", "mediaSingle", "mediaGroup":
		writeBlocks(b, n.Content, indent)
	case "media":
		// Attachments need Jira credentials to open; point at them by name.
		if raw, ok := n.Attrs["alt"]; ok {
			var alt string
			if json.Unmarshal(raw, &alt) == nil && alt != "" {
				b.WriteString(indent + "[attachment: " + alt + "]\n\n")
			}
		}
	default:
		// Unknown block: keep whatever text it holds rather than dropping it.
		if text := inlineText(n.Content); text != "" {
			b.WriteString(indent + text + "\n\n")
		} else if n.Text != "" {
			b.WriteString(indent + n.Text + "\n\n")
		}
	}
}

func writeListItem(b *strings.Builder, item adfNode, indent, marker string) {
	pad := strings.Repeat(" ", len(marker))
	first := true
	for _, child := range item.Content {
		switch child.Type {
		case "paragraph":
			prefix := indent + pad
			if first {
				prefix = indent + marker
				first = false
			}
			b.WriteString(prefix + inlineText(child.Content) + "\n")
		case "bulletList", "orderedList", "taskList":
			var nested strings.Builder
			writeBlock(&nested, child, indent+pad)
			b.WriteString(strings.TrimRight(nested.String(), "\n") + "\n")
		default:
			var nested strings.Builder
			writeBlock(&nested, child, indent+pad)
			b.WriteString(strings.TrimRight(nested.String(), "\n") + "\n")
		}
	}
	if first {
		b.WriteString(indent + marker + "\n")
	}
}

func inlineText(nodes []adfNode) string {
	var b strings.Builder
	for _, n := range nodes {
		switch n.Type {
		case "text":
			b.WriteString(n.Text)
		case "hardBreak":
			b.WriteString("\n")
		case "mention":
			b.WriteString(attrString(n, "text"))
		case "emoji":
			b.WriteString(firstNonEmpty(attrString(n, "text"), attrString(n, "shortName")))
		case "inlineCard":
			b.WriteString(attrString(n, "url"))
		case "status":
			b.WriteString("[" + attrString(n, "text") + "]")
		case "date":
			b.WriteString(attrString(n, "timestamp"))
		default:
			b.WriteString(inlineText(n.Content))
			b.WriteString(n.Text)
		}
	}
	return b.String()
}

func attrString(n adfNode, key string) string {
	raw, ok := n.Attrs[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

var blankRuns = regexp.MustCompile(`\n{3,}`)

func collapseBlankLines(s string) string { return blankRuns.ReplaceAllString(s, "\n\n") }

// acHeading matches a line that starts an acceptance-criteria section, with or
// without Markdown heading marks, bold, a trailing colon, or "AC".
var acHeading = regexp.MustCompile(`(?i)^\s*(?:#{1,6}\s*)?\**\s*(?:acceptance criteria|acceptatiecriteria)\s*:?\s*\**\s*(.*)$`)

// splitAcceptanceCriteria moves an "Acceptance criteria" section out of a
// description when the site has no custom field for it. Everything from that
// heading to the next heading (or the end) is the criteria. A description
// without the section comes back unchanged.
func splitAcceptanceCriteria(description string) (rest, criteria string) {
	lines := strings.Split(description, "\n")
	start := -1
	for i, line := range lines {
		if acHeading.MatchString(line) {
			start = i
			break
		}
	}
	if start < 0 {
		return description, ""
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
			end = i
			break
		}
	}
	var section []string
	if m := acHeading.FindStringSubmatch(lines[start]); m != nil && strings.TrimSpace(m[1]) != "" {
		section = append(section, strings.TrimSpace(m[1]))
	}
	section = append(section, lines[start+1:end]...)
	remaining := append(append([]string(nil), lines[:start]...), lines[end:]...)
	return strings.TrimSpace(collapseBlankLines(strings.Join(remaining, "\n"))), strings.TrimSpace(strings.Join(section, "\n"))
}
