// The entries of the VGUI main menu.
//
// CHudMainMenuOverride::LoadMenuEntries reads this and either drives the panel
// of the same name out of mainmenuoverride.res -- which is where the in-game
// buttons along the bottom come from, and why they are hidden at the main menu
// -- or builds a button for it in the column on the left.
//
// This ships loose rather than in pak1.vpk because the pak is a prebuilt
// download (game_clean/dlpak.sh) and has never carried one; without it the menu
// falls through to Team Fortress' own file, which does not match our panels.
"GameMenu"
{
	// Every entry here names a panel that mainmenuoverride.res already places:
	// the row along the bottom of the menu, and the in-game buttons. An entry
	// whose panel does not exist gets built and stacked into a column down the
	// middle of the screen instead -- which is not where this menu keeps its
	// buttons. They belong along the top, in the matchmaking dashboard, and
	// along the bottom. The middle is for the friends and updates blocks.
	"CharacterSetupButton"
	{
		"label"			"#MMenu_CharacterSetup"
		"command"		"engine open_charinfo"
		"subimage"		"glyph_items"
	}

	"CallVoteButton"
	{
		"label"			""
		"command"		"callvote"
		"subimage"		"icon_checkbox"
		"tooltip"		"#MMenu_CallVote"
		"OnlyInGame"	"1"
	}

	"MutePlayersButton"
	{
		"label"			""
		"command"		"OpenMutePlayerDialog"
		"subimage"		"glyph_muted"
		"tooltip"		"#MMenu_MutePlayers"
		"OnlyInGame"	"1"
	}

	"RequestCoachButton"
	{
		"label"			""
		"command"		"engine cl_coach_find_coach"
		"subimage"		"icon_whistle"
		"tooltip"		"#MMenu_RequestCoach"
		"OnlyInGame"	"1"
	}

	"ReportPlayerButton"
	{
		"label"			""
		"command"		"OpenReportPlayerDialog"
		"subimage"		"glyph_alert"
		"tooltip"		"#MMenu_ReportPlayer"
		"OnlyInGame"	"1"
	}

	"VRModeButton"
	{
		"label"				"#MMenu_VRMode_Activate"
		"command"			"engine vr_toggle"
		"subimage"			"glyph_vr"
		"OnlyWhenVREnabled"	"1"
	}
}
