package gc

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"
)

// AdminHandler serves read-only operational views. It is bound to localhost by
// default: it exposes match rosters and SteamIDs, which is not something to put
// on a public interface.
func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		var out adminState
		s.Inspect(func(srv *Server) { out = srv.snapshotState() })
		writeJSON(w, out)
	})

	mux.HandleFunc("GET /policy", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"version": s.policy.Version,
			"digest":  hex.EncodeToString(s.policy.Digest()),
			"policy":  s.policy,
		})
	})

	mux.HandleFunc("GET /world", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.world.Snapshot())
	})

	return mux
}

type adminState struct {
	Now      time.Time    `json:"now"`
	Stats    Stats        `json:"stats"`
	Sessions []adminSess  `json:"sessions"`
	Matches  []adminMatch `json:"matches"`
	Players  int          `json:"known_players"`
	AuthMode string       `json:"auth_mode"`
}

type adminSess struct {
	SteamID  uint64 `json:"steam_id"`
	Verified bool   `json:"verified"`
	Build    string `json:"build"`
	InQueue  bool   `json:"in_queue"`
	Front    string `json:"front,omitempty"`
	Match    string `json:"match,omitempty"`
	CanHost  bool   `json:"can_host"`
	Pop      uint32 `json:"sdr_pop,omitempty"`
}

type adminMatch struct {
	ID         string      `json:"id"`
	Front      string      `json:"front"`
	Map        string      `json:"map"`
	State      string      `json:"state"`
	Age        string      `json:"age"`
	Host       uint64      `json:"host"`
	Address    string      `json:"connect_address,omitempty"`
	GameServer uint64      `json:"game_server,omitempty"`
	Migrations int         `json:"migrations"`
	Flags      int         `json:"flags"`
	Roster     []adminSlot `json:"roster"`
	Ranking    []string    `json:"host_ranking,omitempty"`
}

type adminSlot struct {
	SteamID   uint64 `json:"steam_id"`
	Side      string `json:"side"`
	Connected bool   `json:"connected"`
	Present   bool   `json:"link_up"`
	RTTMs     uint32 `json:"rtt_ms,omitempty"`
}

// snapshotState must run on the core goroutine (via Inspect).
func (s *Server) snapshotState() adminState {
	out := adminState{
		Now:      time.Now(),
		Stats:    s.stats,
		Players:  s.store.Count(),
		AuthMode: s.auth.Mode(),
	}
	for id, sess := range s.sessions {
		out.Sessions = append(out.Sessions, adminSess{
			SteamID:  id,
			Verified: sess.verified,
			Build:    sess.clientVersion,
			InQueue:  sess.inQueue,
			Front:    sess.frontID,
			Match:    s.byPlayer[id],
			CanHost:  sess.caps.CanHost,
			Pop:      sess.caps.SDRPopID,
		})
	}
	for _, m := range s.matches {
		am := adminMatch{
			ID:         m.ID,
			Front:      m.FrontID,
			Map:        m.Map,
			State:      m.State.String(),
			Age:        time.Since(m.CreatedAt).Round(time.Second).String(),
			Host:       m.HostID,
			Address:    m.ConnectAddress,
			GameServer: m.GameServerSteamID,
			Migrations: m.Migrations,
			Flags:      m.Flags,
		}
		for _, slot := range m.slots() {
			am.Roster = append(am.Roster, adminSlot{
				SteamID:   slot.SteamID,
				Side:      slot.Side.String(),
				Connected: slot.Connected,
				Present:   slot.Present,
				RTTMs:     slot.RTTMs,
			})
		}
		for _, r := range m.Ranking {
			am.Ranking = append(am.Ranking, r.String())
		}
		out.Matches = append(out.Matches, am)
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
