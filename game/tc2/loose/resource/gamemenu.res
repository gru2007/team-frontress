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

	// This panel has a larger, orange treatment in MainMenuOverride.res. The
	// command is handled by CHudMainMenuOverride and opens the native Valve
	// matchmaking dashboard, which also owns the live queue state.
	"FindGameButton"
	{
		"label"			"#MMenu_FindAGame"
		"command"		"find_game"
		"subimage"		"glyph_multiplayer"
		"OnlyAtMenu"	"1"
	}

	"ServerBrowserButton"
	{
		"label"			"#MMenu_BrowseServers"
		"command"		"engine openserverbrowser"
		"subimage"		"glyph_server"
		"OnlyAtMenu"	"1"
	}

	"TrainingButton"
	{
		"label"			"#MMenu_PlayList_Training_Button"
		"command"		"offlinepractice"
		"subimage"		"glyph_practice"
		"OnlyAtMenu"	"1"
	}

	"CreateServerButton"
	{
		"label"			"#MMenu_HostAGame"
		"command"		"engine opencreateserverdialog"
		"subimage"		"glyph_create"
		"OnlyAtMenu"	"1"
	}

	"ChangeServerButton"
	{
		"label"			"#MMenu_PlayMultiplayer"
		"command"		"engine openserverbrowser"
		"subimage"		"glyph_multiplayer"
		"OnlyInGame"	"1"
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
