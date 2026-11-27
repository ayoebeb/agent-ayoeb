package autopilotlearn

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Bounds of the one-time history import. The read is deliberately small and
// strict: only transcripts of orchestrator sessions, only the pairs.
const (
	maxImportFiles     = 300
	maxImportFileBytes = 200 << 20
	maxImportLine      = 8 << 20
	maxImportPairs     = 2000
	// maxHumanAnswerRunes: a longer message is a new task or a spec, not an answer.
	maxHumanAnswerRunes = 1200
)

// ImportResult says what an import found.
type ImportResult struct {
	Files   int `json:"files"`
	Pairs   int `json:"pairs"`
	Added   int `json:"added"`
	Skipped int `json:"skipped"`
}

var orchestratorDir = regexp.MustCompile(`worktrees-(.+?)-orchestrator(?:-|$)`)

// ProjectFromTranscriptDir returns the project of an orchestrator transcript
// folder name (Claude names the folder after the session's working directory),
// and false for any folder that is not an orchestrator's.
func ProjectFromTranscriptDir(name string) (string, bool) {
	m := orchestratorDir.FindStringSubmatch(name)
	if len(m) < 2 || m[1] == "" {
		return "", false
	}
	return m[1], true
}

// DefaultRoots are the places Claude keeps transcripts: the user's own
// ~/.claude/projects and the projects folder of every saved Claude account.
func DefaultRoots(home, dataDir string) []string {
	accounts, _ := filepath.Glob(filepath.Join(dataDir, "agent-accounts", "claude", "*", "projects"))
	roots := make([]string, 0, 1+len(accounts))
	roots = append(roots, filepath.Join(home, ".claude", "projects"))
	return append(roots, accounts...)
}

// ImportTranscripts scans the roots for orchestrator transcripts and returns the
// question and answer pairs found, newest files first. Nothing is written.
func ImportTranscripts(ctx context.Context, roots []string) ([]Pair, ImportResult, error) {
	var res ImportResult
	type file struct {
		path, project string
		mod           time.Time
	}
	var files []file
	seenDir := map[string]bool{}
	seenSession := map[string]bool{}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			project, ok := ProjectFromTranscriptDir(e.Name())
			if !ok {
				continue
			}
			dir := filepath.Join(root, e.Name())
			if resolved, err := filepath.EvalSymlinks(dir); err == nil {
				if seenDir[resolved] {
					continue
				}
				seenDir[resolved] = true
			}
			items, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, it := range items {
				if it.IsDir() || !strings.HasSuffix(it.Name(), ".jsonl") || seenSession[it.Name()] {
					continue
				}
				info, err := it.Info()
				if err != nil || !info.Mode().IsRegular() {
					continue
				}
				if info.Size() > maxImportFileBytes {
					res.Skipped++
					continue
				}
				seenSession[it.Name()] = true
				files = append(files, file{filepath.Join(dir, it.Name()), project, info.ModTime()})
			}
		}
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	if len(files) > maxImportFiles {
		res.Skipped += len(files) - maxImportFiles
		files = files[:maxImportFiles]
	}
	var out []Pair
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return out, res, err
		}
		fh, err := os.Open(f.path)
		if err != nil {
			res.Skipped++
			continue
		}
		pairs, err := ParseTranscript(ctx, fh, f.project, strings.TrimSuffix(filepath.Base(f.path), ".jsonl"))
		_ = fh.Close()
		if err != nil && ctx.Err() != nil {
			return out, res, ctx.Err()
		}
		res.Files++
		out = append(out, pairs...)
		if len(out) >= maxImportPairs {
			out = out[:maxImportPairs]
			break
		}
	}
	res.Pairs = len(out)
	return out, res, nil
}

type transcriptRecord struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Timestamp   string `json:"timestamp"`
	Origin      *struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Input struct {
		Command string `json:"command"`
	} `json:"input"`
	Name string `json:"name"`
}

var (
	waitingCmd = regexp.MustCompile(`notify-user\b[^\n]*?--waiting(?:[=\s]+(?:"((?:[^"\\]|\\.)*)"|'([^']*)'))?`)
	// automationPrefixes start a "human" message that AO or Claude wrote.
	automationPrefixes = []string{"Reports since your previous turn", "Another Claude session sent a message", "<cross-session-message", "<local-command", "<task-notification", "<system-reminder", "[Image:", "[from ", "Caveat:"}
)

// ParseTranscript reads one Claude transcript (JSON lines) of an orchestrator
// session and returns the decisions in it: an assistant turn that ended asking
// the owner something (a declared `notify-user --waiting`, or a closing
// question), followed by the owner's next message.
//
// It is strict on purpose. Only records the owner typed count as answers
// (origin.kind "human", not a side chain, not a report AO injected, not a long
// task description), and a pair is kept only when the turn before it really asked.
func ParseTranscript(ctx context.Context, r io.Reader, project, session string) ([]Pair, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	var (
		out        []Pair
		lastText   string
		waiting    string
		declared   bool
		lines      int
		userMarker = []byte(`"type":"user"`)
		asstMarker = []byte(`"type":"assistant"`)
	)
	for {
		line, err := readBoundedLine(br, maxImportLine)
		if len(line) > 0 && (bytes.Contains(line, userMarker) || bytes.Contains(line, asstMarker)) {
			var rec transcriptRecord
			if json.Unmarshal(line, &rec) == nil && !rec.IsSidechain {
				switch rec.Type {
				case "assistant":
					for _, b := range decodeBlocks(rec.Message.Content) {
						switch b.Type {
						case "text":
							if t := strings.TrimSpace(b.Text); t != "" {
								lastText = t
							}
						case "tool_use":
							if q, ok := waitingQuestion(b); ok {
								waiting, declared = q, true
							}
						}
					}
				case "user":
					if rec.Origin != nil && rec.Origin.Kind == "human" && !rec.IsMeta {
						if answer, ok := humanText(rec.Message.Content); ok {
							if p, ok := buildPair(project, session, lastText, waiting, declared, answer, rec.Timestamp); ok {
								out = append(out, p)
							}
						}
						// Every owner message starts a new turn, answer or not.
						lastText, waiting, declared = "", "", false
					}
				}
			}
		}
		lines++
		if lines%2000 == 0 {
			if cerr := ctx.Err(); cerr != nil {
				return out, cerr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, err
		}
	}
}

// readBoundedLine returns the next line, or nil for a line longer than limit (the
// rest of it is discarded). It returns io.EOF with the last line, if any.
func readBoundedLine(br *bufio.Reader, limit int) ([]byte, error) {
	var buf []byte
	tooLong := false
	for {
		chunk, isPrefix, err := br.ReadLine()
		if !tooLong {
			if len(buf)+len(chunk) > limit {
				tooLong, buf = true, nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if err != nil {
			if tooLong {
				return nil, err
			}
			return buf, err
		}
		if !isPrefix {
			if tooLong {
				return nil, nil
			}
			return buf, nil
		}
	}
}

func decodeBlocks(raw json.RawMessage) []contentBlock {
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		return blocks
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return []contentBlock{{Type: "text", Text: s}}
	}
	return nil
}

// waitingQuestion reads the question out of an `ao notify-user --waiting` call.
func waitingQuestion(b contentBlock) (string, bool) {
	if b.Name != "Bash" && b.Name != "PowerShell" {
		return "", false
	}
	m := waitingCmd.FindStringSubmatch(b.Input.Command)
	if m == nil {
		return "", false
	}
	q := m[1]
	if q == "" {
		q = m[2]
	}
	q = strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(q)
	return strings.TrimSpace(q), true
}

// humanText returns what the owner typed. A message made only of tool results
// is not the owner; a message of AO's own wording is not an answer.
func humanText(raw json.RawMessage) (string, bool) {
	var parts []string
	for _, b := range decodeBlocks(raw) {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" || utf8.RuneCountInString(text) > maxHumanAnswerRunes {
		return "", false
	}
	for _, p := range automationPrefixes {
		if strings.HasPrefix(text, p) {
			return "", false
		}
	}
	return text, true
}

func buildPair(project, session, lastText, waiting string, declared bool, answer, ts string) (Pair, bool) {
	question := waiting
	if question == "" {
		if !closesWithQuestion(lastText) {
			return Pair{}, false
		}
		question = lastQuestion(lastText)
	}
	if !declared && lastText == "" {
		return Pair{}, false
	}
	options, recommendation := ExtractOptions(lastText)
	at, _ := time.Parse(time.RFC3339, ts)
	p := Pair{
		Project: project, Session: session, At: at, Source: SourceImport,
		Question: question, Context: lastText, Options: options, Recommendation: recommendation, Answer: answer,
	}
	return p.Clean()
}

// closesWithQuestion: the assistant's last message ends by asking something.
func closesWithQuestion(text string) bool {
	tail := text
	if r := []rune(tail); len(r) > 300 {
		tail = string(r[len(r)-300:])
	}
	return strings.Contains(tail, "?")
}

// lastQuestion is the last line of the text that asks something.
func lastQuestion(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(mdMarkers.Replace(lines[i])); strings.Contains(l, "?") {
			return l
		}
	}
	return text
}
