// Package reports keeps private moderation reports separately from public
// match history.
//
// Reports about one player add up to a case. A moderator closes a case --
// action taken or dismissed -- and reports filed after that open the next
// one. How a reporter's past cases ended is their standing: reports from
// players whose reports keep leading to action weigh more, reports from
// players whose reports keep being dismissed weigh less. Players who were
// told action was taken against someone they reported learn it from their
// pending feedback.
//
// Every report is its own file, written once. Cases are one file per player
// (cases/), standings one file (reporters.json); both are rewritten whole.
// The store belongs to the GC loop; nothing else may call it concurrently.
package reports

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	ErrCooldown    = errors.New("report rate limit")
	ErrNothingOpen = errors.New("no open reports")
)

// Reasons, as CMsgGC_ReportPlayer::EReason.
const (
	ReasonCheating   = 1
	ReasonIdle       = 2
	ReasonHarassment = 3
	ReasonGriefing   = 4
)

// Detail bits, as CMsgGC_ReportPlayer::EDetail.
const (
	DetailAimbot     = 1
	DetailWallhack   = 2
	DetailTriggerbot = 4
	DetailMovement   = 8
	DetailExploit    = 16
	DetailChat       = 32
	DetailVoice      = 64
	DetailName       = 128

	CheatingDetails   = DetailAimbot | DetailWallhack | DetailTriggerbot | DetailMovement | DetailExploit
	HarassmentDetails = DetailChat | DetailVoice | DetailName
)

// DetailNames are the detail bits by name, in display order.
var DetailNames = []struct {
	Bit  uint32
	Name string
}{
	{DetailAimbot, "aimbot"}, {DetailWallhack, "wallhack"}, {DetailTriggerbot, "triggerbot"},
	{DetailMovement, "movement"}, {DetailExploit, "exploit"},
	{DetailChat, "chat"}, {DetailVoice, "voice"}, {DetailName, "name"},
}

// ReasonName is a reason as the admin API spells it.
func ReasonName(r int32) string {
	switch r {
	case ReasonCheating:
		return "cheating"
	case ReasonIdle:
		return "idle"
	case ReasonHarassment:
		return "harassment"
	case ReasonGriefing:
		return "griefing"
	}
	return "unknown"
}

// Outcomes of a case.
const (
	OutcomeActioned  = "actioned"
	OutcomeDismissed = "dismissed"
)

type Report struct {
	Reporter string    `json:"reporter"`
	Target   string    `json:"target"`
	Reason   int32     `json:"reason"`
	MatchID  string    `json:"match_id,omitempty"`
	At       time.Time `json:"at"`
	Detail   uint32    `json:"detail,omitempty"`
	Comment  string    `json:"comment,omitempty"`
	Source   string    `json:"source,omitempty"`
	KilledBy uint32    `json:"killed_by,omitempty"` // times the target killed the reporter on that map
	Server   string    `json:"server,omitempty"`
	Map      string    `json:"map,omitempty"`
	// Party is the reporter's party when it had other members: a party's
	// reports count once.
	Party string `json:"party,omitempty"`
	// EAC: the game also sent the report to EOS Player Reports.
	EAC bool `json:"eac,omitempty"`
}

// Group is who a report counts for: the reporter's party, or the reporter.
func (r Report) Group() string {
	if r.Party != "" {
		return "party:" + r.Party
	}
	return r.Reporter
}

// Resolution is how a moderator (or Easy Anti-Cheat) closed a case.
type Resolution struct {
	At      time.Time `json:"at"`
	Outcome string    `json:"outcome"`
	By      string    `json:"by"`
	Note    string    `json:"note,omitempty"`
	Reports int       `json:"reports"` // open reports it closed
}

type caseFile struct {
	Target      string       `json:"target"`
	Resolutions []Resolution `json:"resolutions"`
}

// Standing is how a reporter's reports have turned out.
type Standing struct {
	Actioned  int `json:"actioned"`
	Dismissed int `json:"dismissed"`
	// Feedback counts cases closed with action that the reporter has not
	// been told about yet.
	Feedback int `json:"feedback,omitempty"`
}

// Weight is how much a reporter's reports count: 1 for someone new, up to 2
// for reliable reporters, down to 0.3 for those whose reports are dismissed.
func (s Standing) Weight() float64 {
	w := float64(2+2*s.Actioned) / float64(2+s.Actioned+s.Dismissed)
	if w < 0.3 {
		return 0.3
	}
	if w > 2 {
		return 2
	}
	return w
}

// Store belongs to the GC loop. A successful Add means the report is on disk.
type Store struct {
	dir       string
	latest    map[string]time.Time
	recent    map[string][]time.Time
	byTarget  map[string][]Report // oldest first
	cases     map[string]*caseFile
	reporters map[string]*Standing
}

func Open(dir string) (*Store, error) {
	s := &Store{dir: dir, latest: map[string]time.Time{}, recent: map[string][]time.Time{},
		byTarget: map[string][]Report{}, cases: map[string]*caseFile{}, reporters: map[string]*Standing{}}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(filepath.Join(dir, "cases"), 0o700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || entry.Name() == "reporters.json" {
			continue
		}
		var r Report
		if err := readJSON(filepath.Join(dir, entry.Name()), &r); err != nil {
			return nil, fmt.Errorf("report %s: %w", entry.Name(), err)
		}
		s.remember(r)
	}
	for target := range s.byTarget {
		list := s.byTarget[target]
		sort.SliceStable(list, func(i, j int) bool { return list[i].At.Before(list[j].At) })
	}
	cases, err := os.ReadDir(filepath.Join(dir, "cases"))
	if err != nil {
		return nil, err
	}
	for _, entry := range cases {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var c caseFile
		if err := readJSON(filepath.Join(dir, "cases", entry.Name()), &c); err != nil {
			return nil, fmt.Errorf("report case %s: %w", entry.Name(), err)
		}
		s.cases[c.Target] = &c
	}
	if err := readJSON(filepath.Join(dir, "reporters.json"), &s.reporters); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("reporters: %w", err)
	}
	if s.reporters == nil {
		s.reporters = map[string]*Standing{}
	}
	return s, nil
}

func readJSON(path string, into any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, into)
}

func (s *Store) remember(r Report) {
	key := r.Reporter + "/" + r.Target
	if r.At.After(s.latest[key]) {
		s.latest[key] = r.At
	}
	s.recent[r.Reporter] = append(s.recent[r.Reporter], r.At)
	s.byTarget[r.Target] = append(s.byTarget[r.Target], r)
}

func (s *Store) Add(r Report) error {
	if at, ok := s.latest[r.Reporter+"/"+r.Target]; ok && r.At.Sub(at) < 5*time.Minute {
		return ErrCooldown
	}
	recent := s.recent[r.Reporter][:0]
	for _, at := range s.recent[r.Reporter] {
		if r.At.Sub(at) < time.Minute {
			recent = append(recent, at)
		}
	}
	s.recent[r.Reporter] = recent
	if len(recent) >= 5 {
		return ErrCooldown
	}
	if s.dir != "" {
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(s.dir, ".report-*")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		_, writeErr := f.Write(raw)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return err
		}
		if err := os.Rename(f.Name(), f.Name()+".json"); err != nil {
			return err
		}
	}
	s.remember(r)
	return nil
}

// About is every report about a player, newest first.
func (s *Store) About(target string) []Report {
	list := s.byTarget[target]
	out := make([]Report, len(list))
	for i, r := range list {
		out[len(list)-1-i] = r
	}
	return out
}

// OpenReports are the reports filed since the player's case was last
// closed, oldest first.
func (s *Store) OpenReports(target string) []Report {
	list := s.byTarget[target]
	since, ok := s.lastClosed(target)
	if !ok {
		return append([]Report(nil), list...)
	}
	var out []Report
	for _, r := range list {
		if r.At.After(since) {
			out = append(out, r)
		}
	}
	return out
}

func (s *Store) lastClosed(target string) (time.Time, bool) {
	c := s.cases[target]
	if c == nil || len(c.Resolutions) == 0 {
		return time.Time{}, false
	}
	return c.Resolutions[len(c.Resolutions)-1].At, true
}

// Targets is every player anyone has reported.
func (s *Store) Targets() []string {
	out := make([]string, 0, len(s.byTarget))
	for t := range s.byTarget {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Resolutions is how a player's cases were closed, oldest first.
func (s *Store) Resolutions(target string) []Resolution {
	c := s.cases[target]
	if c == nil {
		return nil
	}
	return append([]Resolution(nil), c.Resolutions...)
}

// Standing is a reporter's record.
func (s *Store) Standing(reporter string) Standing {
	if st := s.reporters[reporter]; st != nil {
		return *st
	}
	return Standing{}
}

// Resolve closes a player's open case. Every reporter in it gets credit
// (actioned) or loses weight (dismissed), and with action taken, feedback to
// collect. It returns the reporters.
func (s *Store) Resolve(target, outcome, by, note string, now time.Time) ([]string, error) {
	if outcome != OutcomeActioned && outcome != OutcomeDismissed {
		return nil, fmt.Errorf("unknown outcome %q", outcome)
	}
	if target == "" || strings.Trim(target, "0123456789") != "" {
		return nil, fmt.Errorf("not a player: %q", target) // it names a file
	}
	open := s.OpenReports(target)
	if len(open) == 0 {
		return nil, ErrNothingOpen
	}
	seen := map[string]bool{}
	var reporters []string
	for _, r := range open {
		if !seen[r.Reporter] {
			seen[r.Reporter] = true
			reporters = append(reporters, r.Reporter)
		}
	}

	c := s.cases[target]
	next := caseFile{Target: target}
	if c != nil {
		next.Resolutions = append(next.Resolutions, c.Resolutions...)
	}
	next.Resolutions = append(next.Resolutions, Resolution{At: now, Outcome: outcome, By: by, Note: note, Reports: len(open)})
	if err := s.writeFile(filepath.Join("cases", target+".json"), next); err != nil {
		return nil, err
	}
	s.cases[target] = &next

	standings := s.cloneStandings()
	for _, rep := range reporters {
		st := standings[rep]
		if st == nil {
			st = &Standing{}
			standings[rep] = st
		}
		if outcome == OutcomeActioned {
			st.Actioned++
			st.Feedback++
		} else {
			st.Dismissed++
		}
	}
	if err := s.saveStandings(standings); err != nil {
		return nil, err
	}
	return reporters, nil
}

// TakeFeedback is how many of a reporter's reports led to action since they
// were last told, and forgets it.
func (s *Store) TakeFeedback(reporter string) (int, error) {
	st := s.reporters[reporter]
	if st == nil || st.Feedback == 0 {
		return 0, nil
	}
	n := st.Feedback
	standings := s.cloneStandings()
	standings[reporter].Feedback = 0
	if err := s.saveStandings(standings); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) cloneStandings() map[string]*Standing {
	out := make(map[string]*Standing, len(s.reporters))
	for k, v := range s.reporters {
		c := *v
		out[k] = &c
	}
	return out
}

func (s *Store) saveStandings(standings map[string]*Standing) error {
	if err := s.writeFile("reporters.json", standings); err != nil {
		return err
	}
	s.reporters = standings
	return nil
}

// writeFile replaces a file under the store whole.
func (s *Store) writeFile(name string, v any) error {
	if s.dir == "" {
		return nil
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
