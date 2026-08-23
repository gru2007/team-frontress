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
	"ResumeGameButton"
	{
		"label"			"#MMenu_ResumeGame"
		"command"		"ResumeGame"
		"subimage"		"icon_resume"
		"OnlyInGame"	"1"
	}

	// Find a game, host a game and the server browser are not here: they are
	// the three buttons at the top of the information column, which is built
	// in code and so is always present. Putting them in both places would just
	// give the same command two homes.
	"ChangeServerButton"
	{
		"label"			"#MMenu_PlayMultiplayer"
		"command"		"OpenServerBrowser"
		"subimage"		"glyph_multiplayer"
		"OnlyInGame"	"1"
	}

	// Positioned by mainmenuoverride.res, not by the column.
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
