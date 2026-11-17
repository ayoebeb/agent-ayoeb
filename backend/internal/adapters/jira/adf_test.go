package jira

import (
	"encoding/json"
	"testing"
)

func TestADFToTextKeepsStructure(t *testing.T) {
	doc := `{"type":"doc","version":1,"content":[
		{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Goal"}]},
		{"type":"paragraph","content":[{"type":"text","text":"Ship "},{"type":"text","text":"it","marks":[{"type":"strong"}]},{"type":"hardBreak"},{"type":"mention","attrs":{"text":"@Ada"}}]},
		{"type":"bulletList","content":[
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"first"}]}]},
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"second"}]},
				{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"nested"}]}]}]}]}
		]},
		{"type":"orderedList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"step"}]}]}]},
		{"type":"codeBlock","content":[{"type":"text","text":"go test ./..."}]},
		{"type":"paragraph","content":[{"type":"inlineCard","attrs":{"url":"https://example.com/x"}}]}
	]}`
	want := "## Goal\n\nShip it\n@Ada\n\n- first\n- second\n  - nested\n\n1. step\n\n```\ngo test ./...\n```\n\nhttps://example.com/x"
	if got := adfToText(json.RawMessage(doc)); got != want {
		t.Fatalf("adfToText =\n%q\nwant\n%q", got, want)
	}
}

func TestADFToTextAcceptsPlainStringAndNull(t *testing.T) {
	if got := adfToText(json.RawMessage(`"  plain text  "`)); got != "plain text" {
		t.Fatalf("plain = %q", got)
	}
	if got := adfToText(json.RawMessage(`null`)); got != "" {
		t.Fatalf("null = %q", got)
	}
	if got := adfToText(nil); got != "" {
		t.Fatalf("nil = %q", got)
	}
	if got := adfToText(json.RawMessage(`{not json`)); got != "" {
		t.Fatalf("garbage = %q", got)
	}
}

func TestSplitAcceptanceCriteria(t *testing.T) {
	rest, ac := splitAcceptanceCriteria("Intro\n\nAcceptance criteria:\n- a\n- b\n\n## Out of scope\nnope")
	if rest != "Intro\n\n## Out of scope\nnope" || ac != "- a\n- b" {
		t.Fatalf("rest=%q ac=%q", rest, ac)
	}
	rest, ac = splitAcceptanceCriteria("Just a description")
	if rest != "Just a description" || ac != "" {
		t.Fatalf("rest=%q ac=%q", rest, ac)
	}
	rest, ac = splitAcceptanceCriteria("**Acceptance criteria** it works on mobile")
	if rest != "" || ac != "it works on mobile" {
		t.Fatalf("rest=%q ac=%q", rest, ac)
	}
}
