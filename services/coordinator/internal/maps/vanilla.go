// Package maps is the stock Team Fortress map table: which maps exist and what
// each one plays as.
//
// It is here so a match group can say "koth and payload" instead of listing a
// hundred map names, and so a battlefield pool can be built from a mode rather
// than typed out. The table is data lifted from the game's own
// items_game.txt -- the "rolling_match_tags" on each entry of master_maps_list,
// which is exactly what Valve's casual map-selection panel reads -- plus the
// arena maps, which still ship but carry no tag because casual dropped arena.
//
// Nothing here invents a map. Adding one means the game has to have it.
package maps

// Mode names. These are the game's own tag names, not ours.
const (
	ModeAttackDefend = "attack_defend"
	ModeCP           = "cp" // symmetric five-point
	ModeKOTH         = "koth"
	ModePayload      = "payload"
	ModePayloadRace  = "plr"
	ModeCTF          = "ctf"
	ModeMisc         = "misc" // pd, sd, tc, vsh, medieval
	ModePassTime     = "passtime"
	ModeMannpower    = "mannpower"
	ModeHalloween    = "halloween"
	ModeChristmas    = "christmas"
	ModeArena        = "arena"
)

// Casual is the set of modes Valve's own casual queue offers year-round. The
// seasonal buckets and the novelty modes are deliberately not in it: they are
// available, they are just not on by default.
var Casual = []string{
	ModeAttackDefend, ModeCP, ModeKOTH, ModePayload, ModePayloadRace, ModeCTF, ModeMisc,
}

// Map is one playable map and the mode it plays as.
type Map struct {
	Name string
	Mode string
}

// All is every map the stock game ships that any mode claims, in mode order.
var All = []Map{
	{Name: "cp_altitude", Mode: "attack_defend"},
	{Name: "cp_brew", Mode: "attack_defend"},
	{Name: "cp_cargo", Mode: "attack_defend"},
	{Name: "cp_conifer", Mode: "attack_defend"},
	{Name: "cp_dustbowl", Mode: "attack_defend"},
	{Name: "cp_egypt_final", Mode: "attack_defend"},
	{Name: "cp_fortezza", Mode: "attack_defend"},
	{Name: "cp_fulgur", Mode: "attack_defend"},
	{Name: "cp_gorge", Mode: "attack_defend"},
	{Name: "cp_gravelpit", Mode: "attack_defend"},
	{Name: "cp_hadal", Mode: "attack_defend"},
	{Name: "cp_hardwood_final", Mode: "attack_defend"},
	{Name: "cp_junction_final", Mode: "attack_defend"},
	{Name: "cp_mercenarypark", Mode: "attack_defend"},
	{Name: "cp_mojave", Mode: "attack_defend"},
	{Name: "cp_mossrock", Mode: "attack_defend"},
	{Name: "cp_mountainlab", Mode: "attack_defend"},
	{Name: "cp_overgrown", Mode: "attack_defend"},
	{Name: "cp_premuda", Mode: "attack_defend"},
	{Name: "cp_snowplow", Mode: "attack_defend"},
	{Name: "cp_steel", Mode: "attack_defend"},
	{Name: "cp_sulfur", Mode: "attack_defend"},
	{Name: "cppl_gavle", Mode: "attack_defend"},
	{Name: "ctf_haarp", Mode: "attack_defend"},
	{Name: "cp_5gorge", Mode: "cp"},
	{Name: "cp_badlands", Mode: "cp"},
	{Name: "cp_canaveral_5cp", Mode: "cp"},
	{Name: "cp_coldfront", Mode: "cp"},
	{Name: "cp_fastlane", Mode: "cp"},
	{Name: "cp_foundry", Mode: "cp"},
	{Name: "cp_freight_final1", Mode: "cp"},
	{Name: "cp_granary", Mode: "cp"},
	{Name: "cp_gullywash_final1", Mode: "cp"},
	{Name: "cp_metalworks", Mode: "cp"},
	{Name: "cp_powerhouse", Mode: "cp"},
	{Name: "cp_process_final", Mode: "cp"},
	{Name: "cp_reckoner", Mode: "cp"},
	{Name: "cp_snakewater_final1", Mode: "cp"},
	{Name: "cp_standin_final", Mode: "cp"},
	{Name: "cp_sunshine", Mode: "cp"},
	{Name: "cp_vanguard", Mode: "cp"},
	{Name: "cp_well", Mode: "cp"},
	{Name: "cp_yukon_final", Mode: "cp"},
	{Name: "2koth_abbey", Mode: "koth"},
	{Name: "koth_badlands", Mode: "koth"},
	{Name: "koth_blowout", Mode: "koth"},
	{Name: "koth_boardwalk", Mode: "koth"},
	{Name: "koth_brazil", Mode: "koth"},
	{Name: "koth_cachoeira", Mode: "koth"},
	{Name: "koth_camp_saxton", Mode: "koth"},
	{Name: "koth_cascade", Mode: "koth"},
	{Name: "koth_demolition", Mode: "koth"},
	{Name: "koth_dryfield", Mode: "koth"},
	{Name: "koth_harvest_final", Mode: "koth"},
	{Name: "koth_highpass", Mode: "koth"},
	{Name: "koth_king", Mode: "koth"},
	{Name: "koth_lakeside_final", Mode: "koth"},
	{Name: "koth_lazarus", Mode: "koth"},
	{Name: "koth_mannhole", Mode: "koth"},
	{Name: "koth_megaton", Mode: "koth"},
	{Name: "koth_nucleus", Mode: "koth"},
	{Name: "koth_overcast_final", Mode: "koth"},
	{Name: "koth_probed", Mode: "koth"},
	{Name: "koth_rotunda", Mode: "koth"},
	{Name: "koth_sawmill", Mode: "koth"},
	{Name: "koth_sharkbay", Mode: "koth"},
	{Name: "koth_shorelight", Mode: "koth"},
	{Name: "koth_snowtower", Mode: "koth"},
	{Name: "koth_suijin", Mode: "koth"},
	{Name: "koth_viaduct", Mode: "koth"},
	{Name: "koth_winter_ridge", Mode: "koth"},
	{Name: "pl_aquarius", Mode: "payload"},
	{Name: "pl_badwater", Mode: "payload"},
	{Name: "pl_barnblitz", Mode: "payload"},
	{Name: "pl_borneo", Mode: "payload"},
	{Name: "pl_breadspace", Mode: "payload"},
	{Name: "pl_camber", Mode: "payload"},
	{Name: "pl_cashworks", Mode: "payload"},
	{Name: "pl_citadel", Mode: "payload"},
	{Name: "pl_embargo", Mode: "payload"},
	{Name: "pl_emerge", Mode: "payload"},
	{Name: "pl_enclosure_final", Mode: "payload"},
	{Name: "pl_frontier_final", Mode: "payload"},
	{Name: "pl_goldrush", Mode: "payload"},
	{Name: "pl_hoodoo_final", Mode: "payload"},
	{Name: "pl_odyssey", Mode: "payload"},
	{Name: "pl_patagonia", Mode: "payload"},
	{Name: "pl_phoenix", Mode: "payload"},
	{Name: "pl_pier", Mode: "payload"},
	{Name: "pl_redwood", Mode: "payload"},
	{Name: "pl_rumford_event", Mode: "payload"},
	{Name: "pl_snowycoast", Mode: "payload"},
	{Name: "pl_swiftwater_final1", Mode: "payload"},
	{Name: "pl_thundermountain", Mode: "payload"},
	{Name: "pl_upward", Mode: "payload"},
	{Name: "pl_venice", Mode: "payload"},
	{Name: "plr_bananabay", Mode: "plr"},
	{Name: "plr_hacksaw", Mode: "plr"},
	{Name: "plr_hightower", Mode: "plr"},
	{Name: "plr_nightfall_final", Mode: "plr"},
	{Name: "plr_pipeline", Mode: "plr"},
	{Name: "ctf_2fort", Mode: "ctf"},
	{Name: "ctf_2fort_invasion", Mode: "ctf"},
	{Name: "ctf_applejack", Mode: "ctf"},
	{Name: "ctf_doublecross", Mode: "ctf"},
	{Name: "ctf_frosty", Mode: "ctf"},
	{Name: "ctf_landfall", Mode: "ctf"},
	{Name: "ctf_pelican_peak", Mode: "ctf"},
	{Name: "ctf_penguin_peak", Mode: "ctf"},
	{Name: "ctf_pressure", Mode: "ctf"},
	{Name: "ctf_sawmill", Mode: "ctf"},
	{Name: "ctf_sidewinder", Mode: "ctf"},
	{Name: "ctf_turbine", Mode: "ctf"},
	{Name: "ctf_well", Mode: "ctf"},
	{Name: "cp_burghausen", Mode: "misc"},
	{Name: "cp_degrootkeep", Mode: "misc"},
	{Name: "pd_atom_smash", Mode: "misc"},
	{Name: "pd_selbyen", Mode: "misc"},
	{Name: "pd_watergate", Mode: "misc"},
	{Name: "sd_doomsday", Mode: "misc"},
	{Name: "tc_hydro", Mode: "misc"},
	{Name: "vsh_distillery", Mode: "misc"},
	{Name: "vsh_nucleus", Mode: "misc"},
	{Name: "vsh_skirmish", Mode: "misc"},
	{Name: "vsh_tinyrock", Mode: "misc"},
	{Name: "pass_brickyard", Mode: "passtime"},
	{Name: "pass_district", Mode: "passtime"},
	{Name: "pass_timbertown", Mode: "passtime"},
	{Name: "ctf_foundry", Mode: "mannpower"},
	{Name: "ctf_gorge", Mode: "mannpower"},
	{Name: "ctf_hellfire", Mode: "mannpower"},
	{Name: "ctf_thundermountain", Mode: "mannpower"},
	{Name: "arena_afterlife", Mode: "halloween"},
	{Name: "arena_lumberyard_event", Mode: "halloween"},
	{Name: "arena_perks", Mode: "halloween"},
	{Name: "cp_ambush_event", Mode: "halloween"},
	{Name: "cp_cowerhouse", Mode: "halloween"},
	{Name: "cp_darkmarsh", Mode: "halloween"},
	{Name: "cp_degrootkeep_rats", Mode: "halloween"},
	{Name: "cp_freaky_fair", Mode: "halloween"},
	{Name: "cp_gorge_event", Mode: "halloween"},
	{Name: "cp_lavapit_final", Mode: "halloween"},
	{Name: "cp_manor_event", Mode: "halloween"},
	{Name: "cp_spookeyridge", Mode: "halloween"},
	{Name: "cp_sunshine_event", Mode: "halloween"},
	{Name: "ctf_crasher", Mode: "halloween"},
	{Name: "ctf_doublecross_event", Mode: "halloween"},
	{Name: "ctf_helltrain_event", Mode: "halloween"},
	{Name: "htf_marshlands", Mode: "halloween"},
	{Name: "koth_bagel_event", Mode: "halloween"},
	{Name: "koth_dusker", Mode: "halloween"},
	{Name: "koth_harvest_event", Mode: "halloween"},
	{Name: "koth_lakeside_event", Mode: "halloween"},
	{Name: "koth_los_muertos", Mode: "halloween"},
	{Name: "koth_maple_ridge_event", Mode: "halloween"},
	{Name: "koth_megalo", Mode: "halloween"},
	{Name: "koth_moonshine_event", Mode: "halloween"},
	{Name: "koth_sawmill_event", Mode: "halloween"},
	{Name: "koth_slasher", Mode: "halloween"},
	{Name: "koth_slaughter_event", Mode: "halloween"},
	{Name: "koth_slime", Mode: "halloween"},
	{Name: "koth_synthetic_event", Mode: "halloween"},
	{Name: "koth_toxic", Mode: "halloween"},
	{Name: "koth_undergrove_event", Mode: "halloween"},
	{Name: "koth_viaduct_event", Mode: "halloween"},
	{Name: "pd_circus", Mode: "halloween"},
	{Name: "pd_cursed_cove_event", Mode: "halloween"},
	{Name: "pd_farmageddon", Mode: "halloween"},
	{Name: "pd_mannsylvania", Mode: "halloween"},
	{Name: "pd_monster_bash", Mode: "halloween"},
	{Name: "pd_pit_of_death_event", Mode: "halloween"},
	{Name: "pl_bloodwater", Mode: "halloween"},
	{Name: "pl_corruption", Mode: "halloween"},
	{Name: "pl_fifthcurve_event", Mode: "halloween"},
	{Name: "pl_hasslecastle", Mode: "halloween"},
	{Name: "pl_millstone_event", Mode: "halloween"},
	{Name: "pl_precipice_event_final", Mode: "halloween"},
	{Name: "pl_rumble_event", Mode: "halloween"},
	{Name: "pl_sludgepit_event", Mode: "halloween"},
	{Name: "pl_spineyard", Mode: "halloween"},
	{Name: "pl_terror_event", Mode: "halloween"},
	{Name: "plr_hacksaw_event", Mode: "halloween"},
	{Name: "plr_hightower_event", Mode: "halloween"},
	{Name: "sd_doomsday_event", Mode: "halloween"},
	{Name: "tow_dynamite", Mode: "halloween"},
	{Name: "vsh_outburst", Mode: "halloween"},
	{Name: "zi_atoll", Mode: "halloween"},
	{Name: "zi_blazehattan", Mode: "halloween"},
	{Name: "zi_devastation_final1", Mode: "halloween"},
	{Name: "zi_murky", Mode: "halloween"},
	{Name: "zi_sanitarium", Mode: "halloween"},
	{Name: "zi_woods", Mode: "halloween"},
	{Name: "cp_carrier", Mode: "christmas"},
	{Name: "cp_frostwatch", Mode: "christmas"},
	{Name: "cp_gravelpit_snowy", Mode: "christmas"},
	{Name: "ctf_doublecross_snowy", Mode: "christmas"},
	{Name: "ctf_snowfall_final", Mode: "christmas"},
	{Name: "ctf_turbine_winter", Mode: "christmas"},
	{Name: "koth_krampus", Mode: "christmas"},
	{Name: "pd_galleria", Mode: "christmas"},
	{Name: "pd_nutcracker", Mode: "christmas"},
	{Name: "pd_snowville_event", Mode: "christmas"},
	{Name: "pl_chilly", Mode: "christmas"},
	{Name: "pl_coal_event", Mode: "christmas"},
	{Name: "pl_frostcliff", Mode: "christmas"},
	{Name: "pl_wutville_event", Mode: "christmas"},
	{Name: "plr_cutter", Mode: "christmas"},
	{Name: "plr_matterhorn", Mode: "christmas"},
	{Name: "vsh_maul", Mode: "christmas"},
	{Name: "arena_badlands", Mode: "arena"},
	{Name: "arena_byre", Mode: "arena"},
	{Name: "arena_granary", Mode: "arena"},
	{Name: "arena_lumberyard", Mode: "arena"},
	{Name: "arena_nucleus", Mode: "arena"},
	{Name: "arena_offblast_final", Mode: "arena"},
	{Name: "arena_ravine", Mode: "arena"},
	{Name: "arena_sawmill", Mode: "arena"},
	{Name: "arena_watchtower", Mode: "arena"},
	{Name: "arena_well", Mode: "arena"},
}

var (
	byMode = map[string][]string{}
	byName = map[string]string{}
)

func init() {
	for _, m := range All {
		byMode[m.Mode] = append(byMode[m.Mode], m.Name)
		byName[m.Name] = m.Mode
	}
}

// InModes returns every map belonging to any of the named modes, in table
// order and without duplicates. An unknown mode contributes nothing; callers
// that care should validate with KnownMode first.
func InModes(modes ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range All {
		for _, want := range modes {
			if m.Mode == want && !seen[m.Name] {
				seen[m.Name] = true
				out = append(out, m.Name)
			}
		}
	}
	return out
}

// ModeOf returns the mode a map plays as, or "" if the table does not have it.
// A community map nobody here shipped is not an error -- it is just unknown.
func ModeOf(name string) string { return byName[name] }

// KnownMode reports whether any map claims this mode.
func KnownMode(mode string) bool { return len(byMode[mode]) > 0 }

// Modes returns every mode in the table, in table order.
func Modes() []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range All {
		if !seen[m.Mode] {
			seen[m.Mode] = true
			out = append(out, m.Mode)
		}
	}
	return out
}
