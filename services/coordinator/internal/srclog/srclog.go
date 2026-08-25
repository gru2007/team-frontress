// Package srclog reads a Source dedicated server's console log.
//
// The game server is the only thing that knows a match ended and who won, and
// it will tell anything that asks: `logaddress_add host:port` and it sends
// every log line as a UDP packet. That is a stock convar and an old, stable
// format, which is why the agent uses it instead of a game-side plugin.
//
// Only the handful of lines a match result is made of are parsed. Everything
// else is deliberately ignored: this is not a log analyser, and a line it does
// not understand must never become an event.
package srclog

import (
	"bytes"
	"strconv"
	"strings"
)

// Kind is the sort of thing a line said.
type Kind string

const (
	// KindGameOver is the end of a match.
	KindGameOver Kind = "game_over"
	// KindTeamScore is one team's final score, which the server prints
	// alongside the game over line.
	KindTeamScore Kind = "team_score"
	// KindMapLoad is a map change: whatever we were tracking is over.
	KindMapLoad Kind = "map_load"
	// KindReport is a line the game's own matchmaking backend printed for us
	// -- "[frontress] <event> <matchid> k=v k=v". It is the game reporting a
	// result it computed itself, which is strictly better than anything that
	// can be reconstructed from the console log: it knows who was actually in
	// the match, who abandoned it and what everyone scored.
	KindReport Kind = "report"
	// KindRoundWin is one round inside a match. Reported for completeness;
	// the coordinator only acts on game over.
	KindRoundWin Kind = "round_win"
)

// Event is one thing that happened on the server.
type Event struct {
	Kind Kind
	// Team is "Red" or "Blue" as the server spells it, for scores and wins.
	Team string
	// Score is the team's score, for KindTeamScore.
	Score int
	// Event and Fields carry a KindReport line: the event name and its
	// key=value pairs, unparsed beyond splitting.
	Event  string
	Fields map[string]string
	// Map is the map name, for KindMapLoad.
	Map string
	// Reason is the game over reason, when the server gives one.
	Reason string
}

// Strip removes the UDP framing from a log packet and returns the log line.
//
// The framing is four 0xFF bytes and a type byte: 'R' for a plain log packet
// and 'S' for one from a server using a log secret, which then carries the
// secret before the line. Rather than parse the secret, we skip to the "L "
// timestamp marker that every log line starts with.
func Strip(packet []byte) (string, bool) {
	b := packet
	if len(b) >= 5 && bytes.HasPrefix(b, []byte{0xFF, 0xFF, 0xFF, 0xFF}) {
		b = b[4:]
		switch b[0] {
		case 'R', 'S':
			b = b[1:]
		}
	}
	if i := bytes.Index(b, []byte("L ")); i >= 0 {
		b = b[i:]
	}
	line := strings.TrimRight(string(b), "\x00\r\n")
	if line == "" {
		return "", false
	}
	return line, true
}

// Parse turns one log line into an event, or reports that it is not one we
// care about.
func Parse(line string) (Event, bool) {
	// Everything after the timestamp is the interesting part. The timestamp
	// format is fixed but not worth trusting: find the payload by the ": "
	// that ends it.
	body := line
	if i := strings.Index(line, ": "); i >= 0 && strings.HasPrefix(line, "L ") {
		body = line[i+2:]
	}
	body = strings.TrimSpace(body)

	switch {
	case strings.HasPrefix(body, reportPrefix):
		return parseReport(body)

	case strings.HasPrefix(body, `World triggered "Game_Over"`):
		return Event{Kind: KindGameOver, Reason: quoted(body, "reason")}, true

	case strings.HasPrefix(body, `Team "`):
		// Team "Red" final score "3" with "6" players
		team := firstQuoted(body[len(`Team `):])
		if !strings.Contains(body, "final score") {
			return Event{}, false
		}
		n, err := strconv.Atoi(quoted(body, "final score"))
		if err != nil {
			return Event{}, false
		}
		return Event{Kind: KindTeamScore, Team: team, Score: n}, true

	case strings.HasPrefix(body, `World triggered "Round_Win"`):
		return Event{Kind: KindRoundWin, Team: quoted(body, "winner")}, true

	case strings.HasPrefix(body, "Loading map "):
		return Event{Kind: KindMapLoad, Map: strings.Trim(strings.TrimPrefix(body, "Loading map "), `"`)}, true

	case strings.HasPrefix(body, "Started map "):
		return Event{Kind: KindMapLoad, Map: firstQuoted(body[len("Started map "):])}, true
	}
	return Event{}, false
}

// reportPrefix is the marker CTFMMServer::ReportLine writes. It is a protocol
// between the game DLL and this package; changing it in one place breaks the
// other.
const reportPrefix = "[frontress] "

// parseReport reads "[frontress] <event> <matchid> k=v k=v ...".
//
// Deliberately forgiving about the fields: the game may learn to report more of
// them, and an agent that has not been rebuilt should keep working on the ones
// it does understand rather than dropping the line.
func parseReport(body string) (Event, bool) {
	fields := strings.Fields(strings.TrimPrefix(body, reportPrefix))
	if len(fields) < 2 {
		return Event{}, false
	}

	ev := Event{Kind: KindReport, Event: fields[0], Fields: map[string]string{}}
	ev.Fields["match_id"] = fields[1]
	for _, f := range fields[2:] {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		ev.Fields[k] = v
	}
	return ev, true
}

// quoted returns the quoted string that follows a key, e.g. key "reason" in
//
//	World triggered "Game_Over" reason "Reached Time Limit"
func quoted(body, key string) string {
	i := strings.Index(body, key+" \"")
	if i < 0 {
		return ""
	}
	return firstQuoted(body[i+len(key)+1:])
}

// firstQuoted returns the first "..." in s.
func firstQuoted(s string) string {
	i := strings.Index(s, `"`)
	if i < 0 {
		return ""
	}
	rest := s[i+1:]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// NormalizeTeam maps the several spellings the engine uses onto "RED"/"BLU".
func NormalizeTeam(team string) string {
	switch strings.ToUpper(strings.TrimSpace(team)) {
	case "RED":
		return "RED"
	case "BLUE", "BLU":
		return "BLU"
	}
	return ""
}
