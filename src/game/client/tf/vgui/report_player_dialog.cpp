//========= Copyright Valve Corporation, All rights reserved. ============//
//
// Purpose: 
//
// $NoKeywords: $
//=============================================================================//
#include "cbase.h"
#include "report_player_dialog.h"
#include "frontress_gc.h"
#include "gc_clientsystem.h"
#include "ienginevgui.h"
#include "c_tf_playerresource.h"
#include "c_tf_player.h"
#include "vgui_avatarimage.h"
#include <vgui_controls/ImageList.h>
#include "tf_gamerules.h"
#include "tf/frontress/frontress_anticheat_client.h"
#include "tf_gcmessages.h"
#include "inetchannelinfo.h"
#include "igamesystem.h"
#include "GameEventListener.h"
#include "tf_shareddefs.h"

using namespace vgui;

extern const char *FormatSeconds( int seconds );

// in seconds
static const float MIN_REPORT_INTERVAL = 300.f;

struct ReportedPlayer_t
{
	CSteamID steamID;
	double flReportedTime;
};
CUtlVector< ReportedPlayer_t > vecReportedPlayers;

bool CanReportPlayer( CSteamID steamID, bool bVerbose )
{
	bool bCanReport = true;
	for (int i = 0; i < vecReportedPlayers.Count(); ++i)
	{
		if ( vecReportedPlayers[i].steamID == steamID )
		{
			double flTimeSinceLastReported = Plat_FloatTime() - vecReportedPlayers[i].flReportedTime;
			bCanReport = flTimeSinceLastReported >= MIN_REPORT_INTERVAL;
			if ( !bCanReport && bVerbose )
			{
				float flCooldownTime = MIN_REPORT_INTERVAL - flTimeSinceLastReported;
				ConMsg( "Already reported this player. You can report this player again in %.2f seconds\n", flCooldownTime );
			}
			break;
		}
	}

	return bCanReport;
}

//-----------------------------------------------------------------------------
// Who killed the local player on this map, for reports: "killed you 4 times"
// is shown next to the name, and goes to the GC with the report.
//-----------------------------------------------------------------------------
class CFrontressKillTracker : public CAutoGameSystem, public CGameEventListener
{
public:
	CFrontressKillTracker() : CAutoGameSystem( "CFrontressKillTracker" ), m_kills( DefLessFunc( uint64 ) ) {}

	virtual bool Init() OVERRIDE
	{
		ListenForGameEvent( "player_death" );
		return true;
	}
	virtual void LevelInitPreEntity() OVERRIDE
	{
		m_kills.RemoveAll();
		m_lastKiller = CSteamID();
	}
	virtual void FireGameEvent( IGameEvent *pEvent ) OVERRIDE
	{
		const int iLocal = engine->GetLocalPlayer();
		if ( iLocal <= 0 || engine->GetPlayerForUserID( pEvent->GetInt( "userid" ) ) != iLocal )
			return;
		// A Spy's feigned death is not a kill.
		if ( pEvent->GetInt( "death_flags" ) & TF_DEATH_FEIGN_DEATH )
			return;
		const int iAttacker = engine->GetPlayerForUserID( pEvent->GetInt( "attacker" ) );
		player_info_t pi;
		if ( iAttacker <= 0 || iAttacker == iLocal || !engine->GetPlayerInfo( iAttacker, &pi ) || pi.fakeplayer || !pi.friendsID )
			return;
		const CSteamID id( pi.friendsID, GetUniverse(), k_EAccountTypeIndividual );
		const uint64 key = id.ConvertToUint64();
		const unsigned short i = m_kills.Find( key );
		if ( i == m_kills.InvalidIndex() )
			m_kills.Insert( key, 1 );
		else
			++m_kills[ i ];
		m_lastKiller = id;
	}

	int KillsBy( const CSteamID &id ) const
	{
		const unsigned short i = m_kills.Find( id.ConvertToUint64() );
		return i == m_kills.InvalidIndex() ? 0 : m_kills[ i ];
	}
	CSteamID LastKiller() const { return m_lastKiller; }

private:
	CUtlMap< uint64, int > m_kills;
	CSteamID m_lastKiller;
};
static CFrontressKillTracker s_FrontressKillTracker;

int FrontressReport_KillsBy( const CSteamID &steamID ) { return s_FrontressKillTracker.KillsBy( steamID ); }
CSteamID FrontressReport_LastKiller() { return s_FrontressKillTracker.LastKiller(); }

bool ReportPlayerAccount( CSteamID steamID, int nReason )
{
	return ReportPlayerAccountEx( steamID, nReason, 0, NULL, CMsgGC_ReportPlayer_ESource_kSource_MENU );
}

bool ReportPlayerAccountEx( CSteamID steamID, int nReason, uint32 unDetail, const char *pszComment, int nSource )
{
	if ( !steamID.IsValid() || !steamID.BIndividualAccount() ||
		!steamapicontext || !steamapicontext->SteamUser() || steamID == steamapicontext->SteamUser()->GetSteamID() )
	{
		Warning( "Reporting an invalid steam ID\n" );
		return false;
	}

	if ( !CanReportPlayer( steamID, true ) )
	{
		return false;
	}

	if ( nReason <= CMsgGC_ReportPlayer_EReason_kReason_INVALID || nReason >= CMsgGC_ReportPlayer_EReason_kReason_COUNT )
	{
		Assert( !"Invalid report reason" );
		return false;
	}
	if ( !CMsgGC_ReportPlayer_ESource_IsValid( nSource ) )
		nSource = CMsgGC_ReportPlayer_ESource_kSource_MENU;

	// At most 200 bytes, cut on a UTF-8 character boundary.
	char szComment[ 201 ] = "";
	if ( pszComment )
	{
		V_strncpy( szComment, pszComment, sizeof( szComment ) );
		int n = V_strlen( szComment );
		if ( n == (int)sizeof( szComment ) - 1 )
		{
			while ( n > 0 && ( (unsigned char)szComment[ n - 1 ] & 0xC0 ) == 0x80 )
				--n;
			if ( n > 0 && ( (unsigned char)szComment[ n - 1 ] & 0xC0 ) == 0xC0 )
				--n;
			szComment[ n ] = 0;
		}
	}

	char szMap[ MAX_PATH ] = "";
	if ( engine->GetLevelName() && *engine->GetLevelName() )
		V_FileBase( engine->GetLevelName(), szMap, sizeof( szMap ) );
	const char *pszServer = "";
	INetChannelInfo *pNet = engine->GetNetChannelInfo();
	if ( pNet && !pNet->IsLoopback() && pNet->GetAddress() )
		pszServer = pNet->GetAddress();
	const bool bEAC = FrontressAC_Client_CanReport();

	GCSDK::CProtoBufMsg< CMsgGC_ReportPlayer > msg( k_EMsgGC_ReportPlayer );
	msg.Body().set_account_id_target( steamID.GetAccountID() );
	msg.Body().set_reason( (CMsgGC_ReportPlayer_EReason)nReason );
	msg.Body().set_detail( unDetail );
	if ( szComment[ 0 ] )
		msg.Body().set_comment( szComment );
	msg.Body().set_source( (CMsgGC_ReportPlayer_ESource)nSource );
	msg.Body().set_killed_by_target( FrontressReport_KillsBy( steamID ) );
	msg.Body().set_server_address( pszServer );
	msg.Body().set_map( szMap );
	msg.Body().set_eac_forwarded( bEAC );
	if ( !FrontressGC() || !FrontressGC()->BConnected() || !GCClientSystem()->BSendMessage( msg ) )
		return false;
	ConMsg( "Report submitted to the game coordinator.\n" );
	// Easy Anti-Cheat reviews detections against player reports.
	if ( bEAC )
		FrontressAC_Client_ReportPlayer( steamID, nReason, unDetail, szComment, szMap, pszServer );
	return true;
}

void ConfirmPlayerReport( CSteamID steamID )
{
	FOR_EACH_VEC( vecReportedPlayers, i )
	{
		if ( vecReportedPlayers[i].steamID == steamID )
		{
			vecReportedPlayers[i].flReportedTime = Plat_FloatTime();
			return;
		}
	}
	ReportedPlayer_t reportedPlayer;
	reportedPlayer.steamID = steamID;
	reportedPlayer.flReportedTime = Plat_FloatTime();
	vecReportedPlayers.AddToTail( reportedPlayer );
}

//-----------------------------------------------------------------------------
// Purpose: Constructor
//-----------------------------------------------------------------------------
CReportPlayerDialog::CReportPlayerDialog( vgui::Panel *parent ) : BaseClass( parent, "ReportPlayerDialog" )
{
	SetSize( 320, 270 );
	SetTitle( "#GameUI_ReportPlayerCaps", true );

	m_pReportButton = new Button( this, "ReportButton", "" );
	m_pPlayerList = new ListPanel( this, "PlayerList" );
	m_pPlayerList->SetEmptyListText( "#GameUI_NoOtherPlayersInGame" );
	m_pReasonBox = new ComboBox( this, "ReasonBox", 5, false );

	m_mapAvatarsToImageList.SetLessFunc( DefLessFunc( CSteamID ) );
	m_mapAvatarsToImageList.RemoveAll();
}

//-----------------------------------------------------------------------------
// Purpose: Destructor
//-----------------------------------------------------------------------------
CReportPlayerDialog::~CReportPlayerDialog()
{
}

//-----------------------------------------------------------------------------
// Purpose: 
//-----------------------------------------------------------------------------
void CReportPlayerDialog::ApplySchemeSettings( vgui::IScheme *pScheme )
{
	BaseClass::ApplySchemeSettings( pScheme );
	
	LoadControlSettings( "Resource/ReportPlayerDialog.res" );

	if ( m_pImageList )
	{
		delete m_pImageList;
	}
	m_pImageList = new ImageList( false ); 

	if ( m_pPlayerList )
	{
		m_pPlayerList->DeleteAllItems();

		m_pPlayerList->AddColumnHeader( 0, "medal", "", m_iMedalWidth, ListPanel::COLUMN_IMAGE );

		// Avatars are always displayed at 32x32 regardless of resolution
		m_pPlayerList->AddColumnHeader( 1, "avatar", "", m_iAvatarWidth, ListPanel::COLUMN_IMAGE );

		// The player avatar is always a fixed size, so as we change resolutions we need to vary the size of the name column to adjust the total width of all the columns
		m_nExtraSpace = m_pPlayerList->GetWide() - m_iMedalWidth - m_iAvatarWidth - m_iNameWidth - m_iScoreWidth - m_iTimeWidth;

		m_pPlayerList->AddColumnHeader( 2, "name", "#TF_Scoreboard_Name", m_iNameWidth + m_nExtraSpace );
		m_pPlayerList->AddColumnHeader( 3, "score", "#TF_Scoreboard_Score", m_iScoreWidth );
		m_pPlayerList->AddColumnHeader( 4, "time", "#TF_Connected", m_iTimeWidth );
		
		// doesn't make sense to sort with the images
		m_pPlayerList->SetColumnSortable( 0, false );
		m_pPlayerList->SetColumnSortable( 1, false );

		m_pPlayerList->SetImageList( m_pImageList, false );
		m_pPlayerList->SetVisible( true );
	}
}

//-----------------------------------------------------------------------------
// Purpose: 
//-----------------------------------------------------------------------------
void CReportPlayerDialog::Activate()
{
	BaseClass::Activate();

	if ( m_pPlayerList )
	{
		m_pPlayerList->DeleteAllItems();

		static EUniverse universe = steamapicontext->SteamUtils()->GetConnectedUniverse();

		for ( int playerIndex = 1; playerIndex <= MAX_PLAYERS; playerIndex++ )
		{
			if ( g_PR->IsConnected( playerIndex ) || g_PR->IsValid( playerIndex ) )
			{
				if ( g_TF_PR->GetPlayerConnectionState( playerIndex ) != MM_CONNECTED )
					continue;

				// No need to add local player
				if ( engine->GetLocalPlayer() == playerIndex )
					continue;

				player_info_t pi;
				if ( !engine->GetPlayerInfo( playerIndex, &pi ) )
					continue;
				
				// Don't add bots
				if ( pi.fakeplayer )
					continue;

				// Already reported
				CSteamID steamID( pi.friendsID, universe, k_EAccountTypeIndividual );
				if ( !CanReportPlayer( steamID, false ) )
					continue;

				int nTeam = g_PR->GetTeam( playerIndex );

				KeyValues *pKeyValues = new KeyValues( "data" );
				pKeyValues->SetInt( "index", playerIndex );
				pKeyValues->SetString( "name", g_TF_PR->GetPlayerName( playerIndex ) );
				pKeyValues->SetInt( "score", g_TF_PR->GetTotalScore( playerIndex ) );

				// Update their avatar
				if ( steamapicontext->SteamFriends() && steamapicontext->SteamUtils() )
				{
					if ( pi.friendsID )
					{
						CSteamID steamIDForPlayer( pi.friendsID, 1, GetUniverse(), k_EAccountTypeIndividual );

						// See if we already have that avatar in our list
						int iMapIndex = m_mapAvatarsToImageList.Find( steamIDForPlayer );
						int iImageIndex;
						if ( iMapIndex == m_mapAvatarsToImageList.InvalidIndex() )
						{
							CAvatarImage *pImage = new CAvatarImage();
							pImage->SetAvatarSteamID( steamIDForPlayer );
							pImage->SetAvatarSize( 32, 32 );	// Deliberately non scaling
							iImageIndex = m_pImageList->AddImage( pImage );

							m_mapAvatarsToImageList.Insert( steamIDForPlayer, iImageIndex );
						}
						else
						{
							iImageIndex = m_mapAvatarsToImageList[iMapIndex];
						}

						pKeyValues->SetInt( "avatar", iImageIndex );

						CAvatarImage *pAvIm = ( CAvatarImage * ) m_pImageList->GetImage( iImageIndex );
						pAvIm->UpdateFriendStatus();
					}
				}

				// The medal column is just a place holder for the images that are displayed later
				pKeyValues->SetInt( "medal", 0 );

				if ( pi.fakeplayer )
				{
					pKeyValues->SetString( "time", "#TF_Scoreboard_Bot" );
				}
				else
				{
					pKeyValues->SetString( "time", FormatSeconds( gpGlobals->curtime - g_TF_PR->GetConnectTime( playerIndex ) ) );
				}

				Color clr = g_PR->GetTeamColor( nTeam );
				pKeyValues->SetColor( "cellcolor", clr );

				m_pPlayerList->AddItem( pKeyValues, 0, false, false );

				pKeyValues->deleteThis();
			}
		}
	}

	m_pReasonBox->RemoveAll();
	KeyValues *pKeyValues = new KeyValues( "data" );
	SetDialogVariable( "combo_label", g_pVGuiLocalize->Find( "#GameUI_ReportPlayerReason" ) );
	pKeyValues->SetInt( "reason", 0 );
	m_pReasonBox->AddItem( g_pVGuiLocalize->Find( "GameUI_ReportPlayer_Choose" ), pKeyValues );
	pKeyValues->SetInt( "reason", 1 );
	m_pReasonBox->AddItem( g_pVGuiLocalize->Find( "GameUI_ReportPlayer_Cheating" ), pKeyValues );
	pKeyValues->SetInt( "reason", 2 );
	m_pReasonBox->AddItem( g_pVGuiLocalize->Find( "GameUI_ReportPlayer_Idle" ), pKeyValues );
	pKeyValues->SetInt( "reason", 3 );
	m_pReasonBox->AddItem( g_pVGuiLocalize->Find( "GameUI_ReportPlayer_Harassment" ), pKeyValues );
	pKeyValues->SetInt( "reason", 4 );
	m_pReasonBox->AddItem( g_pVGuiLocalize->Find( "GameUI_ReportPlayer_Griefing" ), pKeyValues );
	m_pReasonBox->SilentActivateItemByRow( 0 );
	pKeyValues->deleteThis();

	m_pPlayerList->InvalidateLayout( true );

	UpdateBadgePanels();
	
	m_pPlayerList->SetSingleSelectedItem( m_pPlayerList->GetItemIDFromRow( 0 ) );
	OnItemSelected();
}

//-----------------------------------------------------------------------------
// Purpose: 
//-----------------------------------------------------------------------------
bool CReportPlayerDialog::IsValidPlayerSelected()
{
	bool bIsValidPlayer = false;

	if ( m_pPlayerList->GetSelectedItemsCount() > 0 )
	{
		KeyValues *pData = m_pPlayerList->GetItem( m_pPlayerList->GetSelectedItem( 0 ) );
		player_info_t pi;
		bIsValidPlayer = engine->GetPlayerInfo( pData->GetInt( "index" ), &pi );
#ifdef _DEBUG
		bIsValidPlayer = bIsValidPlayer && pData->GetInt( "index" ) != engine->GetLocalPlayer();
#else
		bIsValidPlayer = bIsValidPlayer && !pi.fakeplayer && pData->GetInt( "index" ) != engine->GetLocalPlayer();
#endif
	}

	return bIsValidPlayer;
}

//-----------------------------------------------------------------------------
// Purpose: 
//-----------------------------------------------------------------------------
void CReportPlayerDialog::OnCommand( const char *command )
{
	if ( !stricmp( command, "Report" ) )
	{
		ReportPlayer();
	}
	else
	{
		BaseClass::OnCommand( command );
	}
}

//-----------------------------------------------------------------------------
// Purpose: 
//-----------------------------------------------------------------------------
void CReportPlayerDialog::ReportPlayer()
{
	for ( int iSelectedItem = 0; iSelectedItem < m_pPlayerList->GetSelectedItemsCount(); iSelectedItem++ )
	{
		KeyValues *pPlayerData = m_pPlayerList->GetItem( m_pPlayerList->GetSelectedItem( iSelectedItem ) );
		if ( !pPlayerData )
			return;

		Assert( pPlayerData->GetInt( "index" ) );

		// 	INVALID = 0;
		// 	CHEATING = 1;
		// 	IDLE = 2;
		// 	HARASSMENT = 3;
		//	GRIEFING = 4;

		player_info_t pi;
		if ( !engine->GetPlayerInfo( pPlayerData->GetInt( "index" ), &pi ) )
			return;

		CSteamID steamID( pi.friendsID, GetUniverse(), k_EAccountTypeIndividual );
		KeyValues *pReasonData = m_pReasonBox->GetActiveItemUserData();
		int nReason = ( pReasonData ) ? pReasonData->GetInt( "reason", 0 ) : 0;
		if ( ReportPlayerAccount( steamID, nReason ) )
			Close();
		return;
	}

	OnItemSelected();
}

//-----------------------------------------------------------------------------
// Purpose: 
//-----------------------------------------------------------------------------
void CReportPlayerDialog::OnItemSelected()
{
	bool bReportButtonEnabled = IsValidPlayerSelected();
	if ( !bReportButtonEnabled )
	{
		m_pReportButton->SetText( "#GameUI_ReportPlayer" );
	}

	// Reason selected?
	KeyValues *pUserData = m_pReasonBox->GetActiveItemUserData();
	bReportButtonEnabled = bReportButtonEnabled && pUserData && pUserData->GetInt( "reason", 0 ) > 0;
	m_pReportButton->SetEnabled( bReportButtonEnabled );
}

//-----------------------------------------------------------------------------
// Purpose: Called when text changes in combo box
//-----------------------------------------------------------------------------
void CReportPlayerDialog::OnTextChanged( KeyValues *data )
{
	Panel *pPanel = reinterpret_cast< vgui::Panel* >( data->GetPtr( "panel" ) );
	vgui::ComboBox *pComboBox = dynamic_cast< vgui::ComboBox* >( pPanel );
	if ( pComboBox && pComboBox == m_pReasonBox )
	{
		bool bReportButtonEnabled = IsValidPlayerSelected();
		KeyValues *pReasonData = m_pReasonBox->GetActiveItemUserData();
		bReportButtonEnabled = bReportButtonEnabled && pReasonData && pReasonData->GetInt( "reason", 0 ) > 0;
		m_pReportButton->SetEnabled( bReportButtonEnabled );
	}
}

//-----------------------------------------------------------------------------
// Purpose: 
//-----------------------------------------------------------------------------
void CReportPlayerDialog::UpdateBadgePanels()
{
	int iNumPanels = 0;

	const IMatchGroupDescription *pMatchDesc = TFGameRules() ? GetMatchGroupDescription( TFGameRules()->GetCurrentMatchGroup() ) : NULL;
	if ( pMatchDesc && m_pPlayerList )
	{
		if ( TFGameRules()->IsMatchTypeCasual() )
		{
			int parentTall = m_pPlayerList->GetTall();
			CTFBadgePanel *pPanel = NULL;

			for ( int i = 0; i < m_pPlayerList->GetItemCount(); i++ )
			{
				KeyValues *pKeyValues = m_pPlayerList->GetItem( m_pPlayerList->GetItemIDFromRow( i ) );
				if ( !pKeyValues )
					continue;

				int iPlayerIndex = pKeyValues->GetInt( "index" );
				const CSteamID steamID = GetSteamIDForPlayerIndex( iPlayerIndex );
				if ( steamID.IsValid() )
				{
					if ( iNumPanels >= m_pBadgePanels.Count() )
					{
						pPanel = new CTFBadgePanel( this, "BadgePanel" );
						pPanel->MakeReadyForUse();
						pPanel->SetVisible( true );
						pPanel->SetZPos( 9999 );
						m_pBadgePanels.AddToTail( pPanel );
					}
					else
					{
						pPanel = m_pBadgePanels[iNumPanels];
					}

					int x, y, wide, tall;
					m_pPlayerList->GetCellBounds( i, 0, x, y, wide, tall );

					if ( y + tall > parentTall )
						continue;

					if ( !pPanel->IsVisible() )
					{
						pPanel->SetVisible( true );
					}

					int xParent, yParent;
					m_pPlayerList->GetPos( xParent, yParent );

					int nPanelXPos, nPanelYPos, nPanelWide, nPanelTall;
					pPanel->GetBounds( nPanelXPos, nPanelYPos, nPanelWide, nPanelTall );

					if ( ( nPanelXPos != xParent + x )
						|| ( nPanelYPos != yParent + y )
						|| ( nPanelWide != wide )
						|| ( nPanelTall != tall ) )
					{
						pPanel->SetBounds( xParent + x, yParent + y, wide, tall );
						pPanel->InvalidateLayout( true, true );
					}

					pPanel->SetupBadge( pMatchDesc, steamID );
					iNumPanels++;
				}
			}
		}
	}

	// hide any unused images
	for ( int i = iNumPanels; i < m_pBadgePanels.Count(); i++ )
	{
		if ( m_pBadgePanels[i]->IsVisible() )
		{
			m_pBadgePanels[i]->SetVisible( false );
		}
	}
}

//-----------------------------------------------------------------------------
// Purpose: 
//-----------------------------------------------------------------------------
void CReportPlayerDialog::OnThink()
{
	if ( IsVisible() )
	{
		UpdateBadgePanels();
	}

	BaseClass::OnThink();
}
