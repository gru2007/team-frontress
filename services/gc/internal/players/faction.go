package players

import (
	"encoding/json"
	"fmt"
)

// ChooseWarSide is durable before it becomes visible and immutable per campaign.
func (s *Store) ChooseWarSide(id uint64, campaign, side string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if campaign == "" || (side != "RED" && side != "BLU") {
		return fmt.Errorf("invalid campaign or side")
	}
	r := s.recordLocked(id)
	if r.WarCampaign == campaign {
		if r.WarSide == side {
			return nil
		}
		return fmt.Errorf("your faction is locked until the next campaign")
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
