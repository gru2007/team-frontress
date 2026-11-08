//========= Copyright Valve Corporation, All rights reserved. ============//
//
// Purpose: 
//
// $NoKeywords: $
//=============================================================================//
#ifndef REPORT_PLAYER_DIALOG_H
#define REPORT_PLAYER_DIALOG_H
#ifdef _WIN32
#pragma once
#endif

#include "vgui_controls/Frame.h"
#include "vgui_controls/ListPanel.h"
#include "vgui_controls/Button.h"
#include "vgui_controls/ComboBox.h"
#include "tf_badge_panel.h"

class CReportPlayerDialog : public vgui::Frame
{
	DECLARE_CLASS_SIMPLE( CReportPlayerDialog, vgui::Frame );

public:
	CReportPlayerDialog( vgui::Panel *parent );
	~CReportPlayerDialog();

	virtual void ApplySchemeSettings( vgui::IScheme *pScheme ) OVERRIDE;
	virtual void Activate() OVERRIDE;

private:
	MESSAGE_FUNC( OnItemSelected, "ItemSelected" );
	MESSAGE_FUNC_PARAMS( OnTextChanged, "TextChanged", data );
	virtual void OnCommand( const char *command ) OVERRIDE;
	virtual void OnThink() OVERRIDE;
	void UpdateBadgePanels();

	void ReportPlayer();
	bool IsValidPlayerSelected();

	virtual void OnKeyCodePressed( vgui::KeyCode code ) OVERRIDE
	{
		if ( code == KEY_XBUTTON_B )
		{
			Close();
		}
		else
		{
			BaseClass::OnKeyCodePressed( code );
		}
	}

	vgui::ListPanel *m_pPlayerList = nullptr;
	vgui::Button *m_pReportButton = nullptr;
	vgui::ComboBox *m_pReasonBox = nullptr;

	vgui::ImageList				*m_pImageList = nullptr;
	CUtlMap<CSteamID, int>		m_mapAvatarsToImageList;
	CUtlVector< CTFBadgePanel* > m_pBadgePanels;

	int m_nExtraSpace = 0;

	CPanelAnimationVarAliasType( int, m_iMedalWidth, "medal_width", "20", "proportional_int" );
	CPanelAnimationVar( int, m_iAvatarWidth, "avatar_width", "65" );		// Avatar width doesn't scale with resolution
	CPanelAnimationVarAliasType( int, m_iNameWidth, "name_width", "94", "proportional_int" );
	CPanelAnimationVarAliasType( int, m_iScoreWidth, "score_width", "30", "proportional_int" );
	CPanelAnimationVarAliasType( int, m_iTimeWidth, "time_width", "60", "proportional_int" );
};

bool ReportPlayerAccount( CSteamID steamID, int nReason );
// Team Frontress: a report with what the player saw (CMsgGC_ReportPlayer::EDetail
// bits), their own words (at most 200 bytes are sent) and where it was filed
// from (CMsgGC_ReportPlayer::ESource). It goes to the GC and, when the player
// is logged in to EOS, to Easy Anti-Cheat's player reports.
bool ReportPlayerAccountEx( CSteamID steamID, int nReason, uint32 unDetail, const char *pszComment, int nSource );
bool CanReportPlayer( CSteamID steamID, bool bVerbose );
void ConfirmPlayerReport( CSteamID steamID );
// How many times this player killed the local player on the current map.
int FrontressReport_KillsBy( const CSteamID &steamID );
// The player who last killed the local player on this map, if any.
CSteamID FrontressReport_LastKiller();

#endif // REPORT_PLAYER_DIALOG_H
