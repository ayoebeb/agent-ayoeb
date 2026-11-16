package store

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// CommandStartedAtKey is where a command's detail records when it first began
// running. A provider announces a tool call before the approval it may wait on, so
// the row's created_at includes that wait; this is the moment execution began, and
// the only honest start for how long the command took.
const CommandStartedAtKey = "startedAt"

// withCommandStart returns the detail to store for a command write, carrying when the
// command first began running.
//
// previous is the detail already stored for the row (empty for a first write) and
// wasRunning says the stored row was already running. The start is stamped only on
// the write that first moves a row into running, then carried through every later
// rewrite of the detail. A command that never ran under this stamp (it went from
// pending straight to settled, or was already running before the stamp existed) gets
// none, so nothing downstream can mistake a wait for run time.
func withCommandStart(incoming, previous string, wasRunning bool, status domain.ActivityStatus, now time.Time) string {
	start := storedCommandStart(previous)
	if start == "" && status == domain.ActivityStatusRunning && !wasRunning {
		start = now.UTC().Format(time.RFC3339Nano)
	}
	if start == "" {
		return incoming
	}
	detail := map[string]any{}
	if strings.TrimSpace(incoming) != "" {
		decoded, ok := decodeDetailObject(incoming)
		if !ok {
			return incoming
		}
		detail = decoded
	}
	if _, carried := detail[CommandStartedAtKey]; !carried {
		detail[CommandStartedAtKey] = start
	}
	out, err := json.Marshal(detail)
	if err != nil {
		return incoming
	}
	return string(out)
}

// storedCommandStart reads the start a previous write recorded, or "" if it has none.
func storedCommandStart(detail string) string {
	if strings.TrimSpace(detail) == "" {
		return ""
	}
	decoded, ok := decodeDetailObject(detail)
	if !ok {
		return ""
	}
	start, _ := decoded[CommandStartedAtKey].(string)
	return start
}

// decodeDetailObject decodes a detail object keeping numbers exactly as written, so
// rewriting it to add one key does not reformat the rest.
func decodeDetailObject(raw string) (map[string]any, bool) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var detail map[string]any
	if err := decoder.Decode(&detail); err != nil || detail == nil {
		return nil, false
	}
	return detail, true
}
