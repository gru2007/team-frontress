package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/greyline-frontress/coordinator/internal/pool"
	"github.com/greyline-frontress/coordinator/internal/srclog"
)

// battle is what the agent is hosting right now.
type battle struct {
	as        *pool.Assignment
	startedAt time.Time
	// mapLoaded flips when the server reports it started the assigned map.
	mapLoaded bool
	live      bool
	reported  bool
	// scores are the in-game team scores, exactly as the log stated them. The
	// coordinator translates them back into war sides, because on a payload or
	// attack/defend map the attacking side is playing as BLU.
	scores map[srclog.Team]int
	rounds map[srclog.Team]int
	joined map[uint64]bool
}

func newBattle(as *pool.Assignment) *battle {
	return &battle{
		as:        as,
		startedAt: time.Now(),
		scores:    map[srclog.Team]int{},
		rounds:    map[srclog.Team]int{},
		joined:    map[uint64]bool{},
	}
}

func (b *battle) bootDeadline() time.Duration {
	if b.as.BootDeadlineS > 0 {
		return time.Duration(b.as.BootDeadlineS) * time.Second
	}
	return 90 * time.Second
}

// run is the agent's single owner goroutine: every state change happens here,
// so nothing about a battle needs a lock.
func (a *Agent) run(ctx context.Context) {
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	watchdog := time.NewTicker(5 * time.Second)
	defer watchdog.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case cmd := <-a.commands:
			a.handleCommand(ctx, cmd)
		case ev := <-a.logs:
			a.handleLog(ctx, ev)
		case <-heartbeat.C:
			a.heartbeat(ctx)
		case <-watchdog.C:
			a.checkDeadlines(ctx)
		}
	}
}

func (a *Agent) handleCommand(ctx context.Context, cmd pool.Command) {
	switch cmd.Type {
	case pool.CommandAssign:
		if cmd.Assignment == nil {
			a.log.Error("assignment command with no assignment")
			return
		}
		a.startBattle(ctx, cmd.Assignment)
	case pool.CommandAbort:
		if a.battle != nil && a.battle.as.MatchID == cmd.MatchID {
			a.log.Warn("battle aborted by the coordinator", "match", cmd.MatchID, "reason", cmd.Reason)
			a.goIdle(ctx)
		}
	case pool.CommandRoster:
		a.addToRoster(cmd)
	case pool.CommandIdle:
		a.goIdle(ctx)
	case pool.CommandShutdown:
		a.log.Info("coordinator asked this server to leave the pool")
		a.goIdle(ctx)
	default:
		a.log.Warn("unknown command", "type", cmd.Type)
	}
}

// addToRoster seats players who deployed after this battle had already
// started. The console side is additive — greyline_roster_add on top of the
// roster already there — so a repeat of an entry the server has costs nothing
// but a line in its log.
func (a *Agent) addToRoster(cmd pool.Command) {
	if a.battle == nil || a.battle.as.MatchID != cmd.MatchID {
		a.log.Warn("roster update for a battle this server is not running", "match", cmd.MatchID)
		return
	}
	for _, e := range cmd.Roster {
		contract := 0
		if e.Contract {
			contract = 1
		}
		line := fmt.Sprintf("greyline_roster_add %d %s %s %d",
			e.SteamID, strings.ToUpper(e.Team), strings.ToUpper(e.Side), contract)
		if _, err := a.exec("%s", line); err != nil {
			a.log.Error("could not add a player to the running roster",
				"match", cmd.MatchID, "steam_id", e.SteamID, "err", err)
			continue
		}
		a.battle.as.Roster = append(a.battle.as.Roster, e)
	}
	a.log.Info("roster extended", "match", cmd.MatchID, "added", len(cmd.Roster),
		"roster", len(a.battle.as.Roster))
}

// startBattle applies the assignment to the game server: lock it to this
// roster, tell it what part of the war it is running, and load the map.
func (a *Agent) startBattle(ctx context.Context, as *pool.Assignment) {
	a.log.Info("battle assigned", "match", as.MatchID, "map", as.Map, "mode", as.Mode,
		"players", len(as.Roster), "front", as.Briefing.FrontName,
		"stage", fmt.Sprintf("%d/%d %s", as.Briefing.Stage, as.Briefing.StageCount, as.Briefing.StageKind))
	a.battle = newBattle(as)

	for _, cmd := range a.battleCommands(as) {
		if _, err := a.exec("%s", cmd); err != nil {
			a.log.Error("could not configure the battle", "cmd", firstWord(cmd), "err", err)
			a.reportState(ctx, as.MatchID, "failed")
			a.battle = nil
			return
		}
	}
}

// battleCommands is the whole console-side contract for one battle. It is a
// list rather than a script so a failure names the command that failed.
func (a *Agent) battleCommands(as *pool.Assignment) []string {
	cmds := []string{
		// Only the roster gets in. The coordinator handed each of them this
		// password with their assignment.
		fmt.Sprintf("sv_password %q", as.Password),
		// Who may join is the roster and the password above, not whoever the
		// machine's Steam account happens to be friends with. A no-op on a
		// dedicated server that has never heard of the convar.
		"sv_friends_only 0",
		// Turn people away for not being on the roster only when the roster
		// names accounts this server will actually see — see
		// Assignment.VerifiedIdentities. Otherwise the password is the gate.
		fmt.Sprintf("greyline_roster_gate %d", boolToConVar(as.VerifiedIdentities)),
		// The coordinator decides the teams, so the server must not shuffle
		// them back.
		"mp_autoteambalance 0",
		"mp_teams_unbalance_limit 0",
		"sv_pausable 0",
		"mp_forcecamera 0",

		// The war context, replicated so the game announces it to players.
		"greyline_roster_clear",
		fmt.Sprintf("greyline_battle_id %q", as.MatchID),
		fmt.Sprintf("greyline_front_id %q", as.Briefing.FrontID),
		fmt.Sprintf("greyline_front_name %q", as.Briefing.FrontName),
		fmt.Sprintf("greyline_node_id %q", as.Briefing.NodeID),
		fmt.Sprintf("greyline_node_name %q", as.Briefing.NodeName),
		fmt.Sprintf("greyline_campaign %q", as.Briefing.Campaign),
		fmt.Sprintf("greyline_attacker %q", as.Briefing.Attacker),
		fmt.Sprintf("greyline_defender %q", as.Briefing.Defender),
		fmt.Sprintf("greyline_stage %d", as.Briefing.Stage),
		fmt.Sprintf("greyline_stage_count %d", as.Briefing.StageCount),
		fmt.Sprintf("greyline_stage_kind %q", as.Briefing.StageKind),
		fmt.Sprintf("greyline_mobilized %q", as.Briefing.Mobilized),

		// Hold the round until the roster is actually on the server. Without
		// this the map starts the moment it loads and whoever is still on the
		// loading screen misses the opening of a battle the war is counting.
		fmt.Sprintf("greyline_min_players %d", as.MinPlayers),
		fmt.Sprintf("greyline_muster_timeout %d", as.MusterTimeoutS),
	}
	// The roster, so the server can put every mercenary on the side the war put
	// them on rather than letting them pick.
	for _, e := range as.Roster {
		contract := 0
		if e.Contract {
			contract = 1
		}
		// Game team first, then the side of the war they fight for. The two
		// differ on a directional map, and the server tells the player so.
		cmds = append(cmds, fmt.Sprintf("greyline_roster_add %d %s %s %d",
			e.SteamID, strings.ToUpper(e.Team), strings.ToUpper(e.Side), contract))
	}
	cmds = append(cmds, modeRules(as.Mode)...)
	cmds = append(cmds, fmt.Sprintf("changelevel %s", as.Map))
	return cmds
}

func boolToConVar(b bool) int {
	if b {
		return 1
	}
	return 0
}

// modeRules are the win conditions that make a battle end by itself. Without
// them a battle would run until the coordinator's timeout, and the war would
// wait on it.
func modeRules(mode string) []string {
	switch strings.ToLower(mode) {
	case "5cp":
		return []string{"mp_windifference 0", "mp_winlimit 3", "mp_maxrounds 0", "mp_timelimit 30"}
	case "koth":
		return []string{"mp_winlimit 2", "mp_maxrounds 0", "mp_timelimit 30"}
	case "arena":
		return []string{"mp_winlimit 3", "mp_maxrounds 0", "mp_timelimit 0", "tf_arena_use_queue 0"}
	case "payload", "attack_defend", "ad":
		// One round decides it: the attackers either take the objective or run
		// out of time.
		return []string{"mp_winlimit 0", "mp_maxrounds 1", "mp_timelimit 0"}
	case "ctf":
		return []string{"mp_winlimit 3", "mp_maxrounds 0", "mp_timelimit 30"}
	default:
		return []string{"mp_winlimit 3", "mp_timelimit 30"}
	}
}

// handleLog moves the battle along from what the game server says happened.
func (a *Agent) handleLog(ctx context.Context, ev srclog.Event) {
	b := a.battle
	if b == nil {
		return
	}
	switch ev.Kind {
	case srclog.KindMapStarted:
		if !b.mapLoaded && strings.EqualFold(ev.Map, b.as.Map) {
			b.mapLoaded = true
			a.log.Info("battle map up", "match", b.as.MatchID, "map", ev.Map)
			a.reportState(ctx, b.as.MatchID, "ready")
			a.announceBriefing()
		}

	case srclog.KindPlayerJoined:
		if ev.SteamID == 0 {
			return
		}
		b.joined[ev.SteamID] = true
		if !b.live {
			b.live = true
			a.reportState(ctx, b.as.MatchID, "live")
		}

	case srclog.KindRoundWin:
		if ev.Team != srclog.TeamNone {
			b.rounds[ev.Team]++
		}

	case srclog.KindScore:
		if ev.Team != srclog.TeamNone {
			b.scores[ev.Team] = ev.Score
		}

	case srclog.KindGameOver:
		a.finishBattle(ctx, ev.Reason)
	}
}

// finishBattle reports the result and puts the server back in the pool.
func (a *Agent) finishBattle(ctx context.Context, reason string) {
	b := a.battle
	if b == nil || b.reported {
		return
	}
	b.reported = true

	red, blu := b.finalScores()
	outcome := "STALEMATE"
	switch {
	case red > blu:
		outcome = "RED_WIN"
	case blu > red:
		outcome = "BLU_WIN"
	}
	a.log.Info("battle over", "match", b.as.MatchID, "reason", reason,
		"red", red, "blu", blu, "outcome", outcome, "duration", time.Since(b.startedAt).Round(time.Second))

	// These are in-game team scores. The coordinator turns them back into war
	// sides, because on a directional map the attacker played as BLU.
	err := a.post(ctx, "/api/v1/servers/result", map[string]any{
		"match_id":   b.as.MatchID,
		"outcome":    outcome,
		"red_score":  red,
		"blu_score":  blu,
		"players":    len(b.joined),
		"duration_s": int(time.Since(b.startedAt).Seconds()),
	}, nil)
	if err != nil {
		// The war did not hear about this battle. Nothing local can fix that,
		// but it must be loud: it is the one failure that loses a result.
		a.log.Error("could not report the result to the coordinator",
			"match", b.as.MatchID, "outcome", outcome, "err", err)
	}
	a.goIdle(ctx)
}

// finalScores prefers what the log stated as the score, and falls back to
// counting round wins when a mode never printed one.
func (b *battle) finalScores() (red, blu uint32) {
	if b.scores[srclog.TeamRed] > 0 || b.scores[srclog.TeamBlu] > 0 {
		return uint32(b.scores[srclog.TeamRed]), uint32(b.scores[srclog.TeamBlu])
	}
	return uint32(b.rounds[srclog.TeamRed]), uint32(b.rounds[srclog.TeamBlu])
}

// goIdle unlocks the server and sends it back to its waiting map.
func (a *Agent) goIdle(ctx context.Context) {
	a.battle = nil
	for _, cmd := range []string{
		`sv_password ""`,
		"greyline_roster_clear",
		`greyline_battle_id ""`,
		`greyline_front_name ""`,
		fmt.Sprintf("changelevel %s", a.cfg.IdleMap),
	} {
		if _, err := a.exec("%s", cmd); err != nil {
			a.log.Warn("could not reset the server", "cmd", firstWord(cmd), "err", err)
			break
		}
	}
	a.heartbeat(ctx)
}

// announceBriefing asks the game to tell players what this battle is. The game
// does the wording and the localization; the agent only triggers it, so the
// same briefing reads correctly in every language the game ships.
func (a *Agent) announceBriefing() {
	if _, err := a.exec("greyline_announce_briefing"); err != nil {
		a.log.Debug("game did not accept the briefing announcement", "err", err)
	}
}

// checkDeadlines catches a server that never came up.
func (a *Agent) checkDeadlines(ctx context.Context) {
	b := a.battle
	if b == nil || b.mapLoaded || b.reported {
		return
	}
	if time.Since(b.startedAt) > b.bootDeadline() {
		a.log.Error("the game server did not load the battle map in time",
			"match", b.as.MatchID, "map", b.as.Map)
		b.reported = true
		a.reportState(ctx, b.as.MatchID, "failed")
		a.goIdle(ctx)
	}
}

func (a *Agent) reportState(ctx context.Context, matchID, state string) {
	if err := a.post(ctx, "/api/v1/servers/state", map[string]any{
		"match_id": matchID,
		"state":    state,
	}, nil); err != nil {
		a.log.Warn("could not report battle state", "match", matchID, "state", state, "err", err)
	}
}

func (a *Agent) heartbeat(ctx context.Context) {
	status, matchID, mapName, players := "idle", "", a.cfg.IdleMap, 0
	if b := a.battle; b != nil {
		matchID = b.as.MatchID
		mapName = b.as.Map
		players = len(b.joined)
		switch {
		case b.live:
			status = "live"
		default:
			status = "booting"
		}
	}
	if err := a.post(ctx, "/api/v1/servers/heartbeat", map[string]any{
		"status":   status,
		"match_id": matchID,
		"map":      mapName,
		"players":  players,
	}, nil); err != nil {
		a.log.Debug("heartbeat failed", "err", err)
	}
}

func firstWord(cmd string) string {
	if i := strings.IndexByte(cmd, ' '); i > 0 {
		return cmd[:i]
	}
	return cmd
}
