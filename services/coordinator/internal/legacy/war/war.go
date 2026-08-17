// Package war is the coordinator's narrow view of the global war layer.
//
// The coordinator does not own the war simulation and must not grow one. It
// needs exactly three things: somewhere to send players (fronts), the map pool
// and size envelope for a front, and a way to hand back a finished match's
// outcome. Provider is that contract. Everything the strategic layer eventually
// grows — supply, encirclement, multi-stage offensives — lives behind it.
//
// FileProvider is a self-contained implementation so the coordinator runs
// end-to-end today. It is data-driven: the node graph, fronts and map pools all
// come from a JSON world file, with no topology compiled in.
package war

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Side mirrors the wire enum without importing it, so the war layer stays
// independent of the coordinator's protocol.
type Side int32

const (
	SideNone Side = 0
	SideRed  Side = 1
	SideBlu  Side = 2
)

func (s Side) String() string {
	switch s {
	case SideRed:
		return "RED"
	case SideBlu:
		return "BLU"
	default:
		return "NONE"
	}
}

// Opposite returns the other side; SideNone has no opposite.
func (s Side) Opposite() Side {
	switch s {
	case SideRed:
		return SideBlu
	case SideBlu:
		return SideRed
	default:
		return SideNone
	}
}

// Outcome is a finished match's verdict as far as the war cares.
type Outcome int32

const (
	OutcomeUnknown Outcome = iota
	OutcomeRedWin
	OutcomeBluWin
	OutcomeStalemate
	OutcomeAbandoned
)

// Node is a territory on the Greyline Network.
type Node struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Owner    Side     `json:"owner"`
	State    string   `json:"state"`
	Adjacent []string `json:"adjacent"`
}

// MapEntry is one battlefield with the population envelope it plays well at.
type MapEntry struct {
	Map        string `json:"map"`
	Mode       string `json:"mode"`
	MinPlayers int    `json:"min_players"`
	IdealPlyrs int    `json:"ideal_players"`
	MaxPlayers int    `json:"max_players"`
	// Migratable is false for modes whose state is not the team scoreline, and
	// which therefore cannot be stood back up on another host. Absent means
	// true, because most TF2 modes score per round and do restore.
	//
	// Mann vs. Machine is the clear case: its state is the wave number, the
	// credits earned and every upgrade bought, none of which is team score.
	// Restoring "the score" there would silently produce a wrong game.
	// Stopwatch and tournament formats are the other: the time to beat and
	// which side is attacking are part of the match, not the scoreline.
	Migratable *bool `json:"migratable,omitempty"`
}

// CanMigrate reports whether a battle on this map may be moved to a new host.
func (m MapEntry) CanMigrate() bool { return m.Migratable == nil || *m.Migratable }

// Front is an active conflict between two adjacent nodes.
type Front struct {
	ID          string     `json:"id"`
	NodeID      string     `json:"node_id"`
	Name        string     `json:"name"`
	Attacker    Side       `json:"attacker"`
	RedPoints   uint32     `json:"red_points"`
	BluPoints   uint32     `json:"blu_points"`
	PointsToWin uint32     `json:"points_to_win"`
	MapPool     []MapEntry `json:"map_pool"`
	Active      bool       `json:"active"`
}

// Update describes what a reported result did to the war, for the client's
// post-match screen.
type Update struct {
	FrontID      string
	RedPoints    uint32
	BluPoints    uint32
	PointsToWin  uint32
	NodeCaptured bool
	NewOwner     Side
	Headline     string
}

// Provider is everything the coordinator needs from the war layer.
type Provider interface {
	// Snapshot returns the current world, for the global map screen.
	Snapshot() Snapshot
	// ActiveFronts returns the fronts players may currently deploy to, best
	// first. A coordinator with a small population concentrates on the first.
	ActiveFronts() []Front
	// Front looks up one front by ID.
	Front(id string) (Front, bool)
	// SelectMap picks a battlefield from the front's pool that suits the given
	// total player count.
	SelectMap(frontID string, totalPlayers int) (MapEntry, bool)
	// ApplyResult advances the front and returns what changed. Called once per
	// counted match; matches that were disputed or abandoned never reach it.
	ApplyResult(frontID string, outcome Outcome) (Update, error)
	// Save persists state.
	Save() error
}

// Snapshot is an immutable view of the world.
type Snapshot struct {
	Revision uint64
	Nodes    []Node
	Fronts   []Front
}

var ErrNoSuchFront = errors.New("war: no such front")

// ---------------------------------------------------------------------------
// FileProvider
// ---------------------------------------------------------------------------

type worldFile struct {
	Revision uint64  `json:"revision"`
	Nodes    []Node  `json:"nodes"`
	Fronts   []Front `json:"fronts"`
}

// FileProvider keeps world state in memory and persists it to a JSON file.
type FileProvider struct {
	mu    sync.RWMutex
	path  string
	world worldFile
	rng   *rand.Rand
	dirty bool
}

// LoadFile reads a world file. A missing file is an error rather than a silent
// empty world: an operator who mistypes the path should find out immediately,
// not discover an empty map after players are online.
func LoadFile(path string) (*FileProvider, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("war: read world %s: %w", path, err)
	}
	var w worldFile
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("war: parse world %s: %w", path, err)
	}
	if len(w.Fronts) == 0 {
		return nil, fmt.Errorf("war: world %s declares no fronts", path)
	}
	for i, f := range w.Fronts {
		if f.PointsToWin == 0 {
			return nil, fmt.Errorf("war: front %q has points_to_win = 0", f.ID)
		}
		if len(f.MapPool) == 0 {
			return nil, fmt.Errorf("war: front %q has an empty map pool", f.ID)
		}
		for _, m := range f.MapPool {
			if m.MinPlayers <= 0 || m.MaxPlayers < m.MinPlayers {
				return nil, fmt.Errorf("war: front %q map %q has an invalid player envelope", f.ID, m.Map)
			}
		}
		w.Fronts[i] = f
	}
	return &FileProvider{
		path:  path,
		world: w,
		rng:   rand.New(rand.NewSource(time.Now().UnixNano())),
	}, nil
}

func (p *FileProvider) Snapshot() Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return Snapshot{
		Revision: p.world.Revision,
		Nodes:    append([]Node(nil), p.world.Nodes...),
		Fronts:   append([]Front(nil), p.world.Fronts...),
	}
}

func (p *FileProvider) ActiveFronts() []Front {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []Front
	for _, f := range p.world.Fronts {
		if f.Active {
			out = append(out, f)
		}
	}
	// Closest to resolution first: a front one win from flipping is the one
	// players most want to see finished.
	sort.Slice(out, func(i, j int) bool {
		return remaining(out[i]) < remaining(out[j])
	})
	return out
}

func remaining(f Front) uint32 {
	r := f.PointsToWin - min32(f.RedPoints, f.PointsToWin)
	b := f.PointsToWin - min32(f.BluPoints, f.PointsToWin)
	return min32(r, b)
}

func min32(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}

func (p *FileProvider) Front(id string) (Front, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, f := range p.world.Fronts {
		if f.ID == id {
			return f, true
		}
	}
	return Front{}, false
}

// SelectMap prefers maps whose ideal population is closest to what is actually
// queued, so a 4v4 does not get dropped onto a 24-player map.
func (p *FileProvider) SelectMap(frontID string, totalPlayers int) (MapEntry, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var f *Front
	for i := range p.world.Fronts {
		if p.world.Fronts[i].ID == frontID {
			f = &p.world.Fronts[i]
			break
		}
	}
	if f == nil {
		return MapEntry{}, false
	}
	var fits []MapEntry
	for _, m := range f.MapPool {
		if totalPlayers >= m.MinPlayers && totalPlayers <= m.MaxPlayers {
			fits = append(fits, m)
		}
	}
	if len(fits) == 0 {
		return MapEntry{}, false
	}
	sort.Slice(fits, func(i, j int) bool {
		return abs(fits[i].IdealPlyrs-totalPlayers) < abs(fits[j].IdealPlyrs-totalPlayers)
	})
	// Keep some rotation among equally-good maps rather than always the first.
	best := abs(fits[0].IdealPlyrs - totalPlayers)
	n := 0
	for n < len(fits) && abs(fits[n].IdealPlyrs-totalPlayers) == best {
		n++
	}
	return fits[p.rng.Intn(n)], true
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// ApplyResult advances the front by one battle point and flips the node when a
// side reaches the threshold.
func (p *FileProvider) ApplyResult(frontID string, outcome Outcome) (Update, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var f *Front
	for i := range p.world.Fronts {
		if p.world.Fronts[i].ID == frontID {
			f = &p.world.Fronts[i]
			break
		}
	}
	if f == nil {
		return Update{}, fmt.Errorf("%w: %s", ErrNoSuchFront, frontID)
	}

	winner := SideNone
	switch outcome {
	case OutcomeRedWin:
		f.RedPoints++
		winner = SideRed
	case OutcomeBluWin:
		f.BluPoints++
		winner = SideBlu
	case OutcomeStalemate:
		// A stalemate is a real result: it consumes time on the front without
		// moving it. Recorded only as a revision bump.
	default:
		return Update{}, fmt.Errorf("war: outcome %v does not advance a front", outcome)
	}

	up := Update{
		FrontID:     f.ID,
		RedPoints:   f.RedPoints,
		BluPoints:   f.BluPoints,
		PointsToWin: f.PointsToWin,
	}

	if winner != SideNone {
		pts := f.RedPoints
		if winner == SideBlu {
			pts = f.BluPoints
		}
		up.Headline = fmt.Sprintf("%s OFFENSIVE: %d / %d", winner, pts, f.PointsToWin)

		if pts >= f.PointsToWin {
			up.NodeCaptured = true
			up.NewOwner = winner
			up.Headline = fmt.Sprintf("%s CAPTURED %s", winner, f.Name)
			for i := range p.world.Nodes {
				if p.world.Nodes[i].ID == f.NodeID {
					p.world.Nodes[i].Owner = winner
					p.world.Nodes[i].State = winner.String() + "_CONTROL"
				}
			}
			// The front is resolved. It stops accepting deployments; opening
			// the next one is the war layer's job, not the coordinator's.
			f.RedPoints, f.BluPoints = 0, 0
			f.Active = false
		}
	} else {
		up.Headline = fmt.Sprintf("STALEMATE AT %s", f.Name)
	}

	p.world.Revision++
	p.dirty = true
	return up, nil
}

// Save writes the world back to disk atomically, so a crash mid-write cannot
// leave a truncated world file behind.
func (p *FileProvider) Save() error {
	p.mu.Lock()
	if !p.dirty {
		p.mu.Unlock()
		return nil
	}
	raw, err := json.MarshalIndent(p.world, "", "  ")
	p.dirty = false
	p.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p.path); err != nil {
		return err
	}
	_ = filepath.Clean(p.path)
	return nil
}
