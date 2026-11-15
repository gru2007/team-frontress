package players

import (
	"encoding/json"
	"fmt"
)

// ChooseWarSide is durable before it becomes visible. The GC restricts changes
// to players outside a match and matchmaking, rather than an entire campaign.
func (s *Store) ChooseWarSide(id uint64, campaign, side string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if campaign == "" || (side != "RED" && side != "BLU") {
		return fmt.Errorf("invalid campaign or side")
	}
	r := s.recordLocked(id)
	if r.WarCampaign == campaign && r.WarSide == side {
		return nil
	}
	ev := event{Type: "war_side", At: s.now(), SteamID: id, Campaign: campaign, Side: side}
	if s.f != nil {
		raw, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		if _, err = s.f.Write(append(raw, '\n')); err != nil {
			return err
		}
		if err = s.f.Sync(); err != nil {
			return err
		}
	}
	s.apply(ev)
	return nil
}
