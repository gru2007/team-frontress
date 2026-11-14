package clans

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var (
	ErrCreationBanned = errors.New("clan_creation_banned")
	ErrIdentityBanned = errors.New("clan_identity_banned")
	ErrModeration     = errors.New("invalid moderation action")
)

// ModerationAction is an immutable audit record. A deletion retains the roster
// privately, so moderators can review responsibility after the public clan is gone.
type ModerationAction struct {
	Action          string    `json:"action"`
	Clan            string    `json:"clan,omitempty"`
	SteamID         uint64    `json:"steam_id,string,omitempty"`
	Actor           uint64    `json:"actor,string"`
	Reason          string    `json:"reason"`
	At              time.Time `json:"at"`
	RestrictMembers bool      `json:"restrict_members,omitempty"`
	Name            string    `json:"name,omitempty"`
	Tag             string    `json:"tag,omitempty"`
	NewTag          string    `json:"new_tag,omitempty"`
	Motto           string    `json:"motto,omitempty"`
	Members         []Member  `json:"members,omitempty"`
}

type ModerationView struct {
	CreationBans []ModerationAction `json:"creation_bans"`
	BannedClans  []ModerationAction `json:"banned_clans"`
	History      []ModerationAction `json:"history"`
	Persisted    bool               `json:"persisted"`
}

func cloneAction(a ModerationAction) ModerationAction {
	a.Members = append([]Member(nil), a.Members...)
	return a
}

func (s *Store) Moderation() ModerationView {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := ModerationView{CreationBans: []ModerationAction{}, BannedClans: []ModerationAction{}, History: []ModerationAction{}, Persisted: s.f != nil}
	for _, a := range s.creationBans {
		v.CreationBans = append(v.CreationBans, cloneAction(a))
	}
	for _, a := range s.bannedClans {
		v.BannedClans = append(v.BannedClans, cloneAction(a))
	}
	sort.Slice(v.CreationBans, func(i, j int) bool { return v.CreationBans[i].SteamID < v.CreationBans[j].SteamID })
	sort.Slice(v.BannedClans, func(i, j int) bool { return v.BannedClans[i].Clan < v.BannedClans[j].Clan })
	for i := len(s.moderation) - 1; i >= 0 && len(v.History) < 200; i-- {
		v.History = append(v.History, cloneAction(s.moderation[i]))
	}
	return v
}

func (s *Store) CreationBanned(id uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.creationBans[id]
	return ok
}

// Moderate writes the deletion and all associated restrictions as one event.
// Unlike ordinary gameplay writes, moderation must report persistence failures
// before changing state or acknowledging the moderator's decision.
func (s *Store) Moderate(a ModerationAction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a.Reason = strings.TrimSpace(a.Reason)
	if a.Actor == 0 || a.Reason == "" || len(a.Reason) > 300 {
		return ErrModeration
	}
	switch a.Action {
	case "set_tag":
		if a.RestrictMembers || a.SteamID != 0 {
			return ErrModeration
		}
		c := s.clans[a.Clan]
		if c == nil {
			return ErrNoClan
		}
		tag, err := CleanTag(a.NewTag)
		if err != nil {
			return err
		}
		for id, other := range s.clans {
			if id != c.ID && strings.EqualFold(other.Tag, tag) {
				return ErrTaken
			}
		}
		for _, ban := range s.bannedClans {
			if strings.EqualFold(ban.Tag, tag) {
				return ErrIdentityBanned
			}
		}
		a.Name, a.Tag, a.NewTag = c.Name, c.Tag, tag
	case "ban", "delete":
		c := s.clans[a.Clan]
		if c == nil {
			return ErrNoClan
		}
		a.Name, a.Tag, a.Motto, a.Members = c.Name, c.Tag, c.Motto, c.Roster()
	case "unban":
		ban, ok := s.bannedClans[a.Clan]
		if !ok {
			return ErrNoClan
		}
		a.Name, a.Tag = ban.Name, ban.Tag
	case "restrict_creation", "allow_creation":
		if a.SteamID == 0 || a.Clan != "" || a.RestrictMembers {
			return ErrModeration
		}
	default:
		return ErrModeration
	}
	a.At = s.now()
	raw, err := json.Marshal(event{Type: "moderation", At: a.At, Moderation: &a})
	if err != nil {
		return err
	}
	if s.f != nil {
		// Roll a failed append back to its original boundary before another
		// action can append; otherwise a torn line would swallow the next action.
		info, err := s.f.Stat()
		if err != nil {
			return err
		}
		if _, err = s.f.Write(append(raw, '\n')); err == nil {
			err = s.f.Sync()
		}
		if err != nil {
			if rollback := s.f.Truncate(info.Size()); rollback != nil {
				return fmt.Errorf("save moderation: %v; rollback: %w", err, rollback)
			}
			return fmt.Errorf("save moderation: %w", err)
		}
	}
	s.applyModeration(a)
	return nil
}

func (s *Store) applyModeration(a ModerationAction) {
	s.moderation = append(s.moderation, a)
	switch a.Action {
	case "set_tag":
		if c := s.clans[a.Clan]; c != nil {
			c.Tag = a.NewTag
		}
	case "ban", "delete":
		if a.Action == "ban" {
			s.bannedClans[a.Clan] = a
		}
		if a.RestrictMembers {
			for _, m := range a.Members {
				restriction := a
				restriction.SteamID = m.SteamID
				s.creationBans[m.SteamID] = restriction
			}
		}
		if c := s.clans[a.Clan]; c != nil {
			s.disband(c)
		}
	case "unban":
		delete(s.bannedClans, a.Clan)
	case "restrict_creation":
		s.creationBans[a.SteamID] = a
	case "allow_creation":
		delete(s.creationBans, a.SteamID)
	}
}
