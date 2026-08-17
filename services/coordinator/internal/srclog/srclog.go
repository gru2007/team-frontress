// Package srclog reads a Source engine log stream.
//
// The agent needs three facts out of a battle — the map started, players
// arrived, somebody won — and srcds already states all three in its log. Taking
// them from there is what keeps the pool able to run a stock game server: no
// plugin, no patched binary, no second protocol inside the game.
//
// The lines are the standard HL log format, and the ones that matter look like:
//
//	L 08/17/2026 - 21:14:03: Started map "cp_process_final" (CRC "...")
//	L 08/17/2026 - 21:16:44: World triggered "Round_Win" (winner "Red")
//	L 08/17/2026 - 21:16:44: Team "Red" current score "1" with "6" players
//	L 08/17/2026 - 21:31:02: Team "Red" final score "3" with "6" players
//	L 08/17/2026 - 21:31:02: World triggered "Game_Over" reason "Reached Win Limit"
package srclog

import (
	"regexp"
	"strconv"
	"strings"
)

// Kind is what a parsed line says happened.
type Kind string

const (
	KindMapStarted   Kind = "map_started"
	KindRoundWin     Kind = "round_win"
	KindScore        Kind = "score"
	KindGameOver     Kind = "game_over"
	KindPlayerJoined Kind = "player_joined"
	KindPlayerLeft   Kind = "player_left"
)

// Team is an in-game team as the log names it. The log says "Blue" where the
// rest of the game says BLU.
type Team string

const (
	TeamRed  Team = "RED"
	TeamBlu  Team = "BLU"
	TeamNone Team = ""
)

func parseTeam(raw string) Team {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "red":
		return TeamRed
	case "blue", "blu":
		return TeamBlu
	default:
		return TeamNone
	}
}

// Event is one meaningful log line.
type Event struct {
	Kind    Kind
	Team    Team
	Score   int
	Players int
	Final   bool
	Map     string
	SteamID uint64
	Name    string
	Reason  string
	Raw     string
}

var (
	reMapStarted = regexp.MustCompile(`Started map "([^"]+)"`)
	reRoundWin   = regexp.MustCompile(`World triggered "Round_Win" \(winner "([^"]+)"\)`)
	reMiniRound  = regexp.MustCompile(`World triggered "Mini_Round_Win" \(winner "([^"]+)"\)`)
	reScore      = regexp.MustCompile(`Team "([^"]+)" (current|final) score "(\d+)"(?: with "(\d+)" players)?`)
	reGameOver   = regexp.MustCompile(`World triggered "Game_Over" reason "([^"]*)"`)
	rePlayer     = regexp.MustCompile(`"([^"]*)<\d+><([^>]*)><[^>]*>" (entered the game|disconnected)`)
	reSteamID3   = regexp.MustCompile(`^\[U:1:(\d+)\]$`)
)

// Parse reads one log line. The second return is false for the overwhelming
// majority of lines, which are chat, damage and connection noise.
func Parse(line string) (Event, bool) {
	line = strings.TrimRight(line, "\r\n\x00")
	// The UDP log stream prefixes each datagram with 0xFFFFFFFF and a type byte.
	if i := strings.Index(line, "L "); i > 0 && i < 8 {
		line = line[i:]
	}

	switch {
	case reGameOver.MatchString(line):
		m := reGameOver.FindStringSubmatch(line)
		return Event{Kind: KindGameOver, Reason: m[1], Raw: line}, true

	case reRoundWin.MatchString(line):
		m := reRoundWin.FindStringSubmatch(line)
		return Event{Kind: KindRoundWin, Team: parseTeam(m[1]), Raw: line}, true

	case reMiniRound.MatchString(line):
		// A stopwatch or multi-stage map's sub-round. It is not the battle's
		// result, but it is progress worth seeing in the agent's log.
		m := reMiniRound.FindStringSubmatch(line)
		return Event{Kind: KindRoundWin, Team: parseTeam(m[1]), Final: false, Raw: line}, true

	case reScore.MatchString(line):
		m := reScore.FindStringSubmatch(line)
		score, _ := strconv.Atoi(m[3])
		players := 0
		if m[4] != "" {
			players, _ = strconv.Atoi(m[4])
		}
		return Event{
			Kind: KindScore, Team: parseTeam(m[1]), Score: score,
			Players: players, Final: m[2] == "final", Raw: line,
		}, true

	case reMapStarted.MatchString(line):
		m := reMapStarted.FindStringSubmatch(line)
		return Event{Kind: KindMapStarted, Map: m[1], Raw: line}, true

	case rePlayer.MatchString(line):
		m := rePlayer.FindStringSubmatch(line)
		kind := KindPlayerJoined
		if m[3] == "disconnected" {
			kind = KindPlayerLeft
		}
		return Event{Kind: kind, Name: m[1], SteamID: SteamID64(m[2]), Raw: line}, true
	}
	return Event{}, false
}

// SteamID64 converts the log's SteamID3 form into the 64-bit ID the coordinator
// uses. Non-player entries ("Console", "BOT") convert to zero.
func SteamID64(raw string) uint64 {
	m := reSteamID3.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return 0
	}
	account, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		return 0
	}
	return account + 76561197960265728
}
