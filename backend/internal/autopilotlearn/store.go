package autopilotlearn

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// DirName is the folder under the data dir that holds the learning file.
const DirName = "autopilot-learning"

const fileName = "learning.json"

// ErrNotFound reports an unknown pair or rule id.
var ErrNotFound = errors.New("autopilotlearn: not found")

type fileData struct {
	Version int     `json:"version"`
	Pairs   []Pair  `json:"pairs"`
	Profile Profile `json:"profile"`
	// Imported records the one-time history import.
	ImportedAt time.Time `json:"importedAt,omitempty"`
}

// Store keeps the pairs and the profile in one small JSON file under the data
// dir. It is read once and rewritten atomically on every change. A single mutex
// serialises writers: changes are rare (an owner answer, an import, an edit).
type Store struct {
	path  string
	clock func() time.Time

	mu   sync.Mutex
	data fileData
}

// Open loads (or starts) the store under dataDir.
func Open(dataDir string, clock func() time.Time) (*Store, error) {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	dir := filepath.Join(dataDir, DirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("autopilotlearn: create %s: %w", dir, err)
	}
	s := &Store{path: filepath.Join(dir, fileName), clock: clock}
	raw, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		s.data = fileData{Version: 1}
	case err != nil:
		return nil, fmt.Errorf("autopilotlearn: read %s: %w", s.path, err)
	default:
		if err := json.Unmarshal(raw, &s.data); err != nil {
			// A damaged file must not take Autopilot down: keep it aside and start over.
			_ = os.Rename(s.path, s.path+".corrupt")
			s.data = fileData{Version: 1}
		}
	}
	return s, nil
}

// saveLocked writes the file through a temp file and a rename.
func (s *Store) saveLocked() error {
	raw, err := json.Marshal(s.data)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// AddPairs stores the new pairs (duplicates by id are skipped) and returns how
// many were added. The oldest pairs are dropped beyond MaxPairs.
func (s *Store) AddPairs(pairs []Pair) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]bool, len(s.data.Pairs))
	for _, p := range s.data.Pairs {
		seen[p.ID] = true
	}
	added := 0
	for _, p := range pairs {
		c, ok := p.Clean()
		if !ok || seen[c.ID] {
			continue
		}
		if c.At.IsZero() {
			c.At = s.clock()
		}
		seen[c.ID] = true
		s.data.Pairs = append(s.data.Pairs, c)
		added++
	}
	if added == 0 {
		return 0, nil
	}
	sort.SliceStable(s.data.Pairs, func(i, j int) bool { return s.data.Pairs[i].At.Before(s.data.Pairs[j].At) })
	if over := len(s.data.Pairs) - MaxPairs; over > 0 {
		s.data.Pairs = s.data.Pairs[over:]
	}
	return added, s.saveLocked()
}

// Pairs returns a copy of every pair, oldest first.
func (s *Store) Pairs() []Pair {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Pair(nil), s.data.Pairs...)
}

// DeletePair removes one pair.
func (s *Store) DeletePair(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.data.Pairs {
		if p.ID == id {
			s.data.Pairs = append(s.data.Pairs[:i:i], s.data.Pairs[i+1:]...)
			return s.saveLocked()
		}
	}
	return ErrNotFound
}

// MarkFolded records that the profile took these pairs into account.
func (s *Store) MarkFolded(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	for i := range s.data.Pairs {
		if set[s.data.Pairs[i].ID] {
			s.data.Pairs[i].Folded = true
		}
	}
	return s.saveLocked()
}

// Profile returns a copy of the decision profile.
func (s *Store) Profile() Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.data.Profile
	p.Rules = append([]Rule(nil), p.Rules...)
	p.Dismissed = append([]string(nil), p.Dismissed...)
	return p
}

// SetProfile replaces the profile (used by the learner).
func (s *Store) SetProfile(p Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Profile = p
	return s.saveLocked()
}

// SetLastError records why the last profile update failed ("" clears it).
func (s *Store) SetLastError(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Profile.LastError = clip(msg, 300)
	_ = s.saveLocked()
}

// AddRule adds an owner rule. The text is bounded and must not be empty.
func (s *Store) AddRule(scope, text, origin string) (Rule, error) {
	text = clip(text, MaxRuleRunes)
	scope = strings.TrimSpace(scope)
	if text == "" {
		return Rule{}, errors.New("a rule needs text")
	}
	if scope == "" {
		scope = ScopeGlobal
	}
	if origin == "" {
		origin = OriginOwner
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := Rule{ID: "rule_" + uuid.NewString()[:8], Scope: scope, Text: text, Origin: origin, UpdatedAt: s.clock()}
	s.data.Profile.Rules = append(s.data.Profile.Rules, r)
	s.data.Profile.UpdatedAt = r.UpdatedAt
	return r, s.saveLocked()
}

// EditRule changes a rule's text. An edited rule becomes the owner's: the
// learner keeps it as given and never rewrites it.
func (s *Store) EditRule(id, text string) (Rule, error) {
	text = clip(text, MaxRuleRunes)
	if text == "" {
		return Rule{}, errors.New("a rule needs text")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.data.Profile.Rules {
		if r.ID == id {
			r.Text, r.Origin, r.UpdatedAt = text, OriginOwner, s.clock()
			s.data.Profile.Rules[i] = r
			s.data.Profile.UpdatedAt = r.UpdatedAt
			return r, s.saveLocked()
		}
	}
	return Rule{}, ErrNotFound
}

// DeleteRule removes a rule and remembers its text so the learner does not bring
// it back.
func (s *Store) DeleteRule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.data.Profile.Rules {
		if r.ID == id {
			s.data.Profile.Rules = append(s.data.Profile.Rules[:i:i], s.data.Profile.Rules[i+1:]...)
			if r.Origin != OriginOwner {
				s.data.Profile.Dismissed = appendCapped(s.data.Profile.Dismissed, r.Text, 40)
			}
			s.data.Profile.UpdatedAt = s.clock()
			return s.saveLocked()
		}
	}
	return ErrNotFound
}

func appendCapped(list []string, v string, n int) []string {
	list = append(list, v)
	if len(list) > n {
		list = list[len(list)-n:]
	}
	return list
}

// ImportedAt reports when the history import last ran (zero: never).
func (s *Store) ImportedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.ImportedAt
}

// MarkImported records that the history import ran.
func (s *Store) MarkImported() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.ImportedAt = s.clock()
	return s.saveLocked()
}

// Counts reports how many pairs exist and how many wait to be folded in.
func (s *Store) Counts() (total, unfolded int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.data.Pairs {
		if !p.Folded {
			unfolded++
		}
	}
	return len(s.data.Pairs), unfolded
}
