package clans

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModerationReplaysDeletionAndMemberRestrictions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clans.jsonl")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := s.Create(1, "Leader", Settings{Name: "Offending clan", Tag: "BAD", Open: true})
	if err := s.Join(2, "Member", c.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Invite(1, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.Moderate(ModerationAction{Action: "ban", Clan: c.ID, Actor: 99, Reason: "prohibited symbols", RestrictMembers: true}); err != nil {
		t.Fatal(err)
	}
	if s.Get(c.ID) != nil || s.Of(1) != nil || s.Of(2) != nil || len(s.InvitesFor(3)) != 0 {
		t.Fatal("clan references survived deletion")
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []uint64{1, 2} {
		if _, err := s.Create(id, "New", Settings{Name: "Another clan", Tag: "NEW"}); err != ErrCreationBanned {
			t.Fatalf("member %d can recreate: %v", id, err)
		}
	}
	if s.CreationBanned(3) {
		t.Fatal("an invitee was punished as a member")
	}
	for _, set := range []Settings{{Name: "offending CLAN", Tag: "XX"}, {Name: "Another clan", Tag: "bad"}} {
		if _, err := s.Create(3, "Other", set); err != ErrIdentityBanned {
			t.Fatalf("banned identity reused: %v", err)
		}
	}
	v := s.Moderation()
	if !v.Persisted || len(v.History) != 1 || len(v.History[0].Members) != 2 || v.History[0].Actor != 99 || len(v.CreationBans) != 2 {
		t.Fatalf("missing audit: %+v", v)
	}
	v.History[0].Members[0].Name = "tampered"
	if s.Moderation().History[0].Members[0].Name == "tampered" {
		t.Fatal("audit leaks mutable state")
	}
	if err := s.Moderate(ModerationAction{Action: "unban", Clan: c.ID, Actor: 99, Reason: "appeal"}); err != nil {
		t.Fatal(err)
	}
	if !s.CreationBanned(1) || s.Get(c.ID) != nil {
		t.Fatal("unbanning identity changed membership or restrictions")
	}
	if err := s.Moderate(ModerationAction{Action: "allow_creation", SteamID: 1, Actor: 99, Reason: "appeal"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(1, "Leader", Settings{Name: c.Name, Tag: c.Tag, Open: true}); err != nil {
		t.Fatal(err)
	}
	// Restricted players may join an existing clan; their restriction is scoped
	// to creating a replacement, not an implicit ban from all social features.
	if err := s.Join(2, "Member", s.Of(1).ID); err != nil {
		t.Fatalf("unexpected join restriction: %v", err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.CreationBanned(1) || !s.CreationBanned(2) || len(s.Moderation().BannedClans) != 0 {
		t.Fatal("appeal did not persist")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatal("moderation log is not private")
	}
}

func TestModerationAfterTornTailReplays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clans.jsonl")
	if err := os.WriteFile(path, []byte(`{"t":"moderation","moderation":`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Moderate(ModerationAction{Action: "restrict_creation", SteamID: 1, Actor: 99, Reason: "after crash"}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.CreationBanned(1) {
		t.Fatal("acknowledged restriction lost after torn tail")
	}
}

func TestDeleteWithoutBanAndFailedPersistence(t *testing.T) {
	s, _ := Open("")
	c, _ := s.Create(1, "Leader", Settings{Name: "Disposable", Tag: "DEL"})
	if err := s.Moderate(ModerationAction{Action: "delete", Clan: c.ID, Actor: 99, Reason: "cleanup"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(1, "Leader", Settings{Name: c.Name, Tag: c.Tag}); err != nil {
		t.Fatal("delete unexpectedly banned creation", err)
	}
	path := filepath.Join(t.TempDir(), "readonly")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s.f = f
	before := len(s.Moderation().History)
	if err := s.Moderate(ModerationAction{Action: "ban", Clan: s.Of(1).ID, Actor: 99, Reason: "failure", RestrictMembers: true}); err == nil {
		t.Fatal("acknowledged failed write")
	}
	if s.Of(1) == nil || s.CreationBanned(1) || len(s.Moderation().History) != before {
		t.Fatal("failed write changed state")
	}
}
