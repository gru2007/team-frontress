package rcon

import (
	"strconv"
	"strings"
)

// Player is one line of a "status" reply.
type Player struct {
	UserID   int
	Name     string
	SteamID  string // SteamID64 as a decimal string, empty when unparseable
	UniqueID string // what the server printed, e.g. "[U:1:12345]"
	Bot      bool
}

// StatusRoster parses the player table out of a "status" reply.
//
// It is how the agent answers "who was actually in this match", which the
// coordinator cannot know: it hands out a roster and hopes. The lines look
// like
//
//	# userid name uniqueid connected ping loss state
//	#      2 "gru"               [U:1:12345]       01:23  50 0 active
//
// Anything that does not parse is skipped rather than guessed at. A short
// roster is a smaller claim than a wrong one.
func StatusRoster(status string) []Player {
	var out []Player
	for _, line := range strings.Split(status, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if !strings.HasPrefix(line, "#") {
			continue
		}
		rest := strings.TrimSpace(line[1:])
		if rest == "" || strings.HasPrefix(rest, "userid") || strings.HasPrefix(rest, "end") {
			continue
		}
		// The name is quoted, and a player may have put anything inside the
		// quotes -- including a '#' and something that looks like a SteamID.
		open := strings.Index(rest, `"`)
		close := strings.LastIndex(rest, `"`)
		if open < 0 || close <= open {
			continue
		}
		p := Player{Name: rest[open+1 : close]}
		if id, err := strconv.Atoi(strings.TrimSpace(rest[:open])); err == nil {
			p.UserID = id
		}
		fields := strings.Fields(rest[close+1:])
		if len(fields) == 0 {
			continue
		}
		p.UniqueID = fields[0]
		if p.UniqueID == "BOT" {
			p.Bot = true
			out = append(out, p)
			continue
		}
		if id, ok := SteamID64(p.UniqueID); ok {
			p.SteamID = id
		}
		out = append(out, p)
	}
	return out
}

// SteamID64 turns a Source unique id into a SteamID64 decimal string.
//
// Both spellings the engine uses are accepted: the modern "[U:1:12345]" and
// the old "STEAM_0:1:6172". Anything else returns ok=false, because a made-up
// SteamID is worse than a missing one -- it would be recorded against a real
// account that belongs to somebody else.
func SteamID64(uniqueID string) (string, bool) {
	const base = uint64(76561197960265728)
	s := strings.TrimSpace(uniqueID)

	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		parts := strings.Split(s[1:len(s)-1], ":")
		if len(parts) != 3 || parts[0] != "U" {
			return "", false
		}
		acct, err := strconv.ParseUint(parts[2], 10, 64)
		if err != nil {
			return "", false
		}
		return strconv.FormatUint(base+acct, 10), true
	}

	if strings.HasPrefix(s, "STEAM_") {
		parts := strings.Split(s[len("STEAM_"):], ":")
		if len(parts) != 3 {
			return "", false
		}
		y, err1 := strconv.ParseUint(parts[1], 10, 64)
		z, err2 := strconv.ParseUint(parts[2], 10, 64)
		if err1 != nil || err2 != nil {
			return "", false
		}
		return strconv.FormatUint(base+z*2+y, 10), true
	}

	// Already a SteamID64.
	if len(s) == 17 {
		if n, err := strconv.ParseUint(s, 10, 64); err == nil && n > base {
			return s, true
		}
	}
	return "", false
}

// TagValue pulls a value out of a convar reply, e.g. from
//
//	"sv_tags" = "tfmm:9f2c" ( def. "" )
//
// which is what `rcon sv_tags` answers with.
func TagValue(reply string) string {
	i := strings.Index(reply, "=")
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(reply[i+1:])
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	rest = rest[1:]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}
