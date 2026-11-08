//========= Team Frontress ====================================================//
//
// The menu's social side. See fui_social.h.
//
//=============================================================================
#include "cbase.h"

// Source's Assert and min/max macros would rewrite RmlUi's declarations.
#pragma push_macro( "Assert" )
#undef Assert
#include "tier0/valve_minmax_off.h"
#include "fui.h"
#include "tier0/valve_minmax_on.h"
#pragma pop_macro( "Assert" )

#include "fui_social.h"
#include "fui_tf.h"
#include "fui_host.h"

#include "frontress_gc.h"
#include "tf_gc_client.h"
#include "tf_party.h"
#include "tf_partyclient.h"
#include "vgui/report_player_dialog.h"
#include "frontress_anticheat_client.h"
#include "c_tf_playerresource.h"
#include "tf_gcmessages.h"
#include "voice_status.h"
#include "ienginevgui.h"
#include "steam/steam_api.h"
#include "vgui/ILocalize.h"

#include <time.h>
#include <algorithm>

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

namespace
{

using fui::Node;

const uint32 k_EMsgGCFrontress_Social = 51010;		// services/gc/internal/gc/social.go
const size_t k_nLinesKept = 160;
const size_t k_nLinesShown = 40;

std::string UTF8Name( const CSteamID &id )
{
	ISteamFriends *pFriends = steamapicontext ? steamapicontext->SteamFriends() : NULL;
	if ( !pFriends )
		return std::string();
	if ( steamapicontext->SteamUser() && id == steamapicontext->SteamUser()->GetSteamID() )
		return pFriends->GetPersonaName();
	return pFriends->GetFriendPersonaName( id );
}

CSteamID Me()
{
	return ( steamapicontext && steamapicontext->SteamUser() ) ? steamapicontext->SteamUser()->GetSteamID() : CSteamID();
}

bool Send( const Node &op )
{
	if ( !FrontressGC() || !FrontressGC()->BConnected() || !GCClientSystem() )
		return false;
	GCSDK::CProtoBufMsg< CMsgPartyChatMsg > msg( k_EMsgGCFrontress_Social );
	msg.Body().set_msg( op.Dump().c_str() );
	return GCClientSystem()->BSendMessage( msg );
}

Node Op( const char *pszOp )
{
	Node op;
	op[ "op" ] = pszOp;
	return op;
}

std::string Str( const Node *p, const char *pszKey )
{
	const Node *v = p ? p->Find( pszKey ) : NULL;
	return v ? v->AsString() : std::string();
}

double Num( const Node *p, const char *pszKey, double flDefault = 0.0 )
{
	const Node *v = p ? p->Find( pszKey ) : NULL;
	return v ? v->AsNumber( flDefault ) : flDefault;
}

bool Bool( const Node *p, const char *pszKey )
{
	const Node *v = p ? p->Find( pszKey ) : NULL;
	return v && v->AsBool();
}

// Steam's answer to "do you approve this purchase" (the overlay dialog the
// coordinator's InitTxn opened). The coordinator finalizes approved orders.
class CMicroTxnListener
{
public:
	CMicroTxnListener() : m_Auth( this, &CMicroTxnListener::OnAuthorization ) {}

	std::vector< std::pair< uint64, bool > > m_Answers;

private:
	void OnAuthorization( MicroTxnAuthorizationResponse_t *pInfo )
	{
		m_Answers.push_back( { pInfo->m_ulOrderID, pInfo->m_bAuthorized != 0 } );
	}
	CCallback< CMicroTxnListener, MicroTxnAuthorizationResponse_t, false > m_Auth;
};

struct Social_t
{
	std::vector< std::string > events;		// from the coordinator, not yet applied
	Node lines;								// every chat line kept, oldest first
	std::string tab = "global";
	bool bViewDirty = true;
	bool bHelloSent = false;
	bool bConnected = false;
	int nUnreadClan = 0, nUnreadParty = 0;
	std::string myTag;
	bool bMeSupporter = false;
	CMicroTxnListener *pTxn = nullptr;
	float flNextPresence = 0.f;
	std::string pendingOrder;
	double flSupportDeadline = 0.;
	double flReportDeadline = 0.;
	std::string reportTarget;
	int nReportSource = 0;		// CMsgGC_ReportPlayer::ESource
};

Social_t &S()
{
	static Social_t s_Social;
	return s_Social;
}

void RefreshInvites( fui::Runtime &ui, bool bFriends )
{
	Node incoming, friends, outgoing, requests;
	incoming.SetArray();
	friends.SetArray();
	outgoing.SetArray();
	requests.SetArray();
	if ( auto *pParty = GTFPartyClient() )
	{
		for ( int i = 0; i < pParty->GetNumIncomingInvites(); ++i )
		{
			const auto *pInvite = pParty->GetIncomingInvite( i );
			if ( !pInvite )
				continue;
			const CSteamID id = pInvite->GetInviter();
			Node &entry = incoming.Append();
			entry[ "id" ] = std::to_string( id.ConvertToUint64() );
			entry[ "name" ] = UTF8Name( id );
		}
		for ( int i = 0; i < pParty->GetNumOutgoingInvites(); ++i )
		{
			const CSteamID id = pParty->GetOutgoingInvite( i );
			Node &entry = outgoing.Append();
			entry[ "id" ] = std::to_string( id.ConvertToUint64() );
			entry[ "name" ] = UTF8Name( id );
		}
		for ( int i = 0; i < pParty->GetNumIncomingJoinRequests(); ++i )
		{
			const CSteamID id = pParty->GetIncomingJoinRequest( i );
			Node &entry = requests.Append();
			entry[ "id" ] = std::to_string( id.ConvertToUint64() );
			entry[ "name" ] = UTF8Name( id );
		}
		auto *pFriends = steamapicontext ? steamapicontext->SteamFriends() : nullptr;
		if ( bFriends && pFriends )
		{
			const int count = pFriends->GetFriendCount( k_EFriendFlagImmediate );
			for ( int i = 0; i < count; ++i )
			{
				const CSteamID id = pFriends->GetFriendByIndex( i, k_EFriendFlagImmediate );
				if ( !id.IsValid() || !pParty->BCanInvitePlayer( id ) || pParty->BHaveOutgoingInviteForSteamID( id ) )
					continue;
				const Node *pSearch = ui.Get( "invites.search" );
				const std::string search = pSearch ? pSearch->AsString() : std::string();
				const std::string playerName = UTF8Name( id );
				if ( !search.empty() && !V_stristr( playerName.c_str(), search.c_str() ) )
					continue;
				Node &entry = friends.Append();
				entry[ "id" ] = std::to_string( id.ConvertToUint64() );
				entry[ "name" ] = playerName;
				entry[ "online" ] = pFriends->GetFriendPersonaState( id ) != k_EPersonaStateOffline;
			}
		}
	}
	ui.Set( "invites.incoming", std::move( incoming ) );
	ui.Set( "invites.outgoing", std::move( outgoing ) );
	ui.Set( "invites.requests", std::move( requests ) );
	if ( bFriends )
		ui.Set( "invites.friends", std::move( friends ) );
}

void SupportError( fui::Runtime &ui, const char *key )
{
	Social_t &s = S();
	s.flSupportDeadline = 0.;
	s.pendingOrder.clear();
	ui.Set( "support.busy", Node( false ) );
	ui.Set( "support.result", Node( "error" ) );
	ui.Set( "support.error", Node( key ) );
}

void ReportOverlay( const char *reason )
{
	const Social_t &s = S();
	Warning( "[frontress support] %s (order %s)\n", reason, s.pendingOrder.c_str() );
	Node report = Op( "support_overlay" );
	report[ "status" ] = reason;
	report[ "order" ] = s.pendingOrder;
	Send( report );
}

std::string Clock( double at )
{
	time_t t = (time_t)at;
	struct tm *lt = localtime( &t );
	if ( !lt )
		return std::string();
	char sz[ 16 ];
	V_snprintf( sz, sizeof( sz ), "%02d:%02d", lt->tm_hour, lt->tm_min );
	return sz;
}

// One line, as the documents show it.
void AddLine( const Node &in )
{
	Social_t &s = S();
	if ( s.lines.IsNull() )
		s.lines.SetArray();
	const double id = Num( &in, "id" );
	for ( size_t i = s.lines.Size(); i-- > 0; )
	{
		if ( s.lines.At( i )->Find( "id" )->AsNumber() == id )
			return;		// a resend after a reconnect
		if ( s.lines.At( i )->Find( "id" )->AsNumber() < id )
			break;
	}
	// Make room first: trimming replaces the array, which would leave a
	// reference to the new line dangling.
	if ( s.lines.Size() >= k_nLinesKept )
	{
		Node trimmed;
		trimmed.SetArray();
		for ( size_t i = s.lines.Size() - k_nLinesKept + 1; i < s.lines.Size(); ++i )
			trimmed.Append( s.lines.At( i )->Clone() );
		s.lines.Assign( std::move( trimmed ) );
	}
	Node &l = s.lines.Append();
	l[ "id" ] = id;
	const std::string ch = Str( &in, "ch" );
	l[ "ch" ] = ch.empty() ? "global" : ch;
	l[ "from" ] = Str( &in, "from" );
	l[ "name" ] = Str( &in, "name" );
	l[ "tag" ] = Str( &in, "tag" );
	l[ "gold" ] = Bool( &in, "gold" );
	l[ "sup" ] = Bool( &in, "sup" );
	l[ "text" ] = Str( &in, "text" );
	l[ "key" ] = Str( &in, "key" );
	l[ "sys" ] = !Str( &in, "key" ).empty();
	l[ "kind" ] = Str( &in, "kind" );
	l[ "time" ] = Clock( Num( &in, "at" ) );
	l[ "me" ] = Str( &in, "from" ) == std::to_string( Me().ConvertToUint64() );
	const Node *pArgs = in.Find( "args" );
	for ( int i = 0; i < 4; ++i )
	{
		const Node *pArg = pArgs ? pArgs->At( i ) : NULL;
		l[ "a" + std::to_string( i + 1 ) ] = pArg ? pArg->AsString() : std::string();
	}
	if ( !l[ "sys" ].AsBool() && !l[ "me" ].AsBool() )
	{
		if ( ch == "clan" && s.tab != "clan" )
			++s.nUnreadClan;
		if ( ch == "party" && s.tab != "party" )
			++s.nUnreadParty;
	}
	s.bViewDirty = true;
}

void ApplyEvent( fui::Runtime &ui, const std::string &json )
{
	Social_t &s = S();
	Node ev;
	if ( !Node::Parse( json, ev ) || !ev.IsObject() )
		return;
	const std::string t = Str( &ev, "t" );

	if ( t == "chat" )
	{
		if ( Bool( &ev, "reset" ) )
		{
			s.lines.SetArray();
			s.nUnreadClan = s.nUnreadParty = 0;
		}
		if ( const Node *pLines = ev.Find( "lines" ) )
		{
			for ( size_t i = 0; i < pLines->Size(); ++i )
				AddLine( *pLines->At( i ) );
		}
	}
	else if ( t == "online" )
	{
		Node *pOnline = ev.Find( "online" );
		if ( pOnline )
		{
			const std::string me = std::to_string( Me().ConvertToUint64() );
			if ( Node *pPlayers = pOnline->Find( "players" ) )
			{
				for ( size_t i = 0; i < pPlayers->Size(); ++i )
				{
					Node &p = *pPlayers->At( i );
					p[ "me" ] = Str( &p, "id" ) == me;
					// The mode they queue for or play, as the play screen names it.
					const int group = (int)Num( &p, "group" );
					p[ "mode" ] = group == k_eTFMatchGroup_Ladder_6v6 ? "ranked" : "casual";
				}
			}
			ui.Set( "social.online", pOnline->Clone() );
			ui.Set( "app.online", Node( Num( pOnline, "total" ) ) );
		}
	}
	else if ( t == "clan" )
	{
		Node clan;
		clan[ "has" ] = ev.Find( "clan" ) && ev.Find( "clan" )->IsObject();
		if ( clan[ "has" ].AsBool() )
		{
			Node &c = clan[ "c" ];
			c.Assign( ev.Find( "clan" )->Clone() );
			s.myTag = Str( &c, "tag" );
			const std::string role = Str( &c, "role" );
			clan[ "officer" ] = role == "leader" || role == "officer";
			clan[ "leader" ] = role == "leader";
			// The settings form starts from what the clan has.
			ui.Set( "clanform.motto", Node( Str( &c, "motto" ) ) );
			ui.Set( "clanform.emblem", Node( Str( &c, "emblem" ) ) );
			ui.Set( "clanform.color", Node( Str( &c, "color" ) ) );
			ui.Set( "clanform.open", Node( Bool( &c, "open" ) ) );
		}
		else
		{
			s.myTag.clear();
			clan[ "c" ].SetObject();
			clan[ "officer" ] = false;
			clan[ "leader" ] = false;
		}
		clan[ "invites" ].Assign( ev.Find( "invites" ) ? ev.Find( "invites" )->Clone() : Node() );
		if ( !clan[ "invites" ].IsArray() )
			clan[ "invites" ].SetArray();
		clan[ "top" ].Assign( ev.Find( "top" ) ? ev.Find( "top" )->Clone() : Node() );
		if ( !clan[ "top" ].IsArray() )
			clan[ "top" ].SetArray();
		clan[ "loaded" ] = true;
		ui.Set( "clan", std::move( clan ) );
		ui.Set( "player.tag", Node( s.myTag ) );
		s.bViewDirty = true;
	}
	else if ( t == "war_faction" )
	{
		ui.Set( "player.war_side", Node( Str( &ev, "side" ) ) );
		ui.Set( "player.war_campaign", Node( Str( &ev, "campaign" ) ) );
	}
	else if ( t == "supporter" )
	{
		s.bMeSupporter = Bool( &ev, "supporter" );
		ui.Set( "player.sup", Node( s.bMeSupporter ) );
		ui.Set( "player.sup_monthly", Node( Bool( &ev, "monthly" ) ) );
		ui.Set( "support.offered", Node( Bool( &ev, "available" ) ) );
	}
	else if ( t == "support" )
	{
		Node doc = ev.Clone();
		doc[ "loading" ] = false;
		doc[ "busy" ] = s.flSupportDeadline > 0.;
		if ( const Node *pError = ui.Get( "support.error" ) )
			doc[ "error" ] = pError->AsString();
		doc[ "show_wall" ] = !ui.Get( "support.show_wall" ) || ui.Get( "support.show_wall" )->AsBool();
		doc[ "offered" ] = true;
		if ( const Node *pAnon = ui.Get( "support.anon" ) )
			doc[ "anon" ] = pAnon->AsBool();
		doc[ "result" ] = ui.Get( "support.result" ) ? ui.Get( "support.result" )->AsString() : std::string();
		if ( Bool( &ev, "available" ) == false && !ev.Find( "once" ) )
		{
			doc[ "offered" ] = false;
			doc[ "once" ].SetArray();
			doc[ "monthly" ].SetArray();
			doc[ "wall" ].SetArray();
		}
		ui.Set( "support", std::move( doc ) );
	}
	else if ( t == "support_pending" )
	{
		s.pendingOrder = Str( &ev, "order" );
		s.flSupportDeadline = Plat_FloatTime() + 120.;
		ui.Set( "support.busy", Node( true ) );
		ui.Set( "support.result", Node( "pending" ) );
	}
	else if ( t == "support_result" )
	{
		s.pendingOrder.clear();
		s.flSupportDeadline = 0.;
		ui.Set( "support.busy", Node( false ) );
		const bool bOK = Bool( &ev, "ok" );
		const std::string kind = Str( &ev, "kind" );
		ui.Set( "support.result", Node( bOK ? ( kind == "cancelled" ? "cancelled" : "thanks" ) : "error" ) );
		ui.Set( "support.error", Node( Str( &ev, "error" ) ) );
		// On the support screen its result bar says the same.
		if ( bOK && kind != "cancelled" && ui.Screen() != "support" )
			FuiTF().Toast( "support", ui.Translate( "fui_support_thanks_title" ), ui.Translate( kind == "monthly" ? "fui_support_thanks_monthly" : "fui_support_thanks_once" ), "" );
	}
	else if ( t == "match_left" )
		GTFGCClientSystem()->ConfirmMatchLeave( V_atoui64( Str( &ev, "lobby" ).c_str() ) );
	else if ( t == "report_saved" )
	{
		const std::string target = Str( &ev, "target" );
		ConfirmPlayerReport( CSteamID( V_atoui64( target.c_str() ) ) );
		const Node *modal = ui.Get( "ui.modal" );
		const bool bShown = modal && modal->AsString() == "report";
		if ( target == s.reportTarget )
		{
			s.flReportDeadline = 0.;
			ui.Set( "report.pending", Node( false ) );
			// The dialog says it itself, and offers to mute them.
			ui.Set( "report.sent", Node( true ) );
		}
		if ( !bShown || target != s.reportTarget )
			FuiTF().Toast( "party", ui.Translate( "fui_report_title" ), ui.Translate( "fui_report_saved" ), "" );
	}
	else if ( t == "report_feedback" )
	{
		// Never who: the coordinator does not say, so a cheater cannot learn
		// who reported them.
		const int n = (int)Num( &ev, "count", 1 );
		std::string body = ui.Translate( n > 1 ? "fui_report_feedback_many" : "fui_report_feedback_one" );
		const size_t at = body.find( "%1" );
		if ( at != std::string::npos )
			body.replace( at, 2, std::to_string( n ) );
		FuiTF().Toast( "quest", ui.Translate( "fui_report_feedback_title" ), body, "" );
	}
	else if ( t == "error" )
	{
		if ( Str( &ev, "key" ).find( "fui_err_report_" ) == 0 )
		{
			s.flReportDeadline = 0.;
			ui.Set( "report.pending", Node( false ) );
		}
		FuiTF().Toast( "error", ui.Translate( "fui_err_title" ), ui.Translate( Str( &ev, "key" ) ), "" );
	}
	else if ( t == "toast" )
	{
		const Node *pArgs = ev.Find( "args" );
		std::string body = ui.Translate( Str( &ev, "key" ) );
		for ( int i = 0; pArgs && i < (int)pArgs->Size(); ++i )
		{
			const std::string mark = "%" + std::to_string( i + 1 );
			const size_t at = body.find( mark );
			if ( at != std::string::npos )
				body.replace( at, mark.size(), pArgs->At( i )->AsString() );
		}
		FuiTF().Toast( Str( &ev, "kind" ), ui.Translate( "fui_toast_clan_title" ), body, "" );
	}
}

// The lines of the open tab, newest last.
void RefreshView( fui::Runtime &ui )
{
	Social_t &s = S();
	if ( !s.bViewDirty )
		return;
	s.bViewDirty = false;
	std::vector< const Node * > picked;
	for ( size_t i = s.lines.IsArray() ? s.lines.Size() : 0; i-- > 0 && picked.size() < k_nLinesShown; )
	{
		const Node *l = s.lines.At( i );
		if ( Str( l, "ch" ) == s.tab )
			picked.push_back( l );
	}
	Node view;
	view.SetArray();
	for ( size_t i = picked.size(); i-- > 0; )
		view.Append( picked[ i ]->Clone() );
	ui.Set( "social.view", std::move( view ) );
	ui.Set( "social.unread_clan", Node( s.nUnreadClan ) );
	ui.Set( "social.unread_party", Node( s.nUnreadParty ) );
	ui.Set( "social.has_clan", Node( !s.myTag.empty() ) );
	const CTFParty *pParty = GTFPartyClient() ? GTFPartyClient()->GetActiveParty() : NULL;
	ui.Set( "social.has_party", Node( pParty && pParty->GetNumMembers() > 1 ) );
}

std::string Digits( const std::string &in )
{
	std::string out;
	for ( char c : in )
	{
		if ( c >= '0' && c <= '9' )
			out += c;
	}
	return out;
}

std::string Word( const std::string &in )
{
	std::string out;
	for ( char c : in )
	{
		if ( isalnum( (unsigned char)c ) || c == '-' || c == '_' )
			out += c;
	}
	return out;
}

std::string Path( fui::Runtime &ui, const char *pszPath )
{
	const Node *p = ui.Get( pszPath );
	return p ? p->AsString() : std::string();
}


//-----------------------------------------------------------------------------
// Reports. The dialog (shell.rml, ui.modal 'report') lists the players on the
// server -- those who killed you first -- then asks why, what exactly, and
// for your own words. The report goes to the coordinator, which puts it in
// front of moderators next to what Easy Anti-Cheat recorded, and to EOS
// Player Reports when you are logged in to EOS.
//-----------------------------------------------------------------------------
struct ReportDetail_t
{
	const char *pszID;
	int nReason;
	uint32 unBit;
};
const ReportDetail_t k_ReportDetails[] = {
	{ "aimbot", CMsgGC_ReportPlayer_EReason_kReason_CHEATING, CMsgGC_ReportPlayer_EDetail_kDetail_AIMBOT },
	{ "wallhack", CMsgGC_ReportPlayer_EReason_kReason_CHEATING, CMsgGC_ReportPlayer_EDetail_kDetail_WALLHACK },
	{ "triggerbot", CMsgGC_ReportPlayer_EReason_kReason_CHEATING, CMsgGC_ReportPlayer_EDetail_kDetail_TRIGGERBOT },
	{ "movement", CMsgGC_ReportPlayer_EReason_kReason_CHEATING, CMsgGC_ReportPlayer_EDetail_kDetail_MOVEMENT },
	{ "exploit", CMsgGC_ReportPlayer_EReason_kReason_CHEATING, CMsgGC_ReportPlayer_EDetail_kDetail_EXPLOIT },
	{ "chat", CMsgGC_ReportPlayer_EReason_kReason_HARASSMENT, CMsgGC_ReportPlayer_EDetail_kDetail_CHAT },
	{ "voice", CMsgGC_ReportPlayer_EReason_kReason_HARASSMENT, CMsgGC_ReportPlayer_EDetail_kDetail_VOICE },
	{ "name", CMsgGC_ReportPlayer_EReason_kReason_HARASSMENT, CMsgGC_ReportPlayer_EDetail_kDetail_NAME },
};

const char *ReportClassID( int iClass )
{
	static const char *s_rgpszIDs[] = { "", "scout", "sniper", "soldier", "demoman", "medic", "heavy", "pyro", "spy", "engineer" };
	return ( iClass > TF_CLASS_UNDEFINED && iClass < (int)ARRAYSIZE( s_rgpszIDs ) ) ? s_rgpszIDs[ iClass ] : "";
}

int ReportPlayerIndex( const CSteamID &id )
{
	for ( int i = 1; i <= gpGlobals->maxClients; ++i )
	{
		player_info_t info;
		if ( engine->GetPlayerInfo( i, &info ) && !info.fakeplayer && info.friendsID == id.GetAccountID() )
			return i;
	}
	return 0;
}

void ReportSelect( fui::Runtime &ui, const std::string &target )
{
	const CSteamID id( V_atoui64( target.c_str() ) );
	const int iPlayer = id.IsValid() ? ReportPlayerIndex( id ) : 0;
	std::string name;
	player_info_t info;
	if ( iPlayer && engine->GetPlayerInfo( iPlayer, &info ) )
		name = info.name;
	ui.Set( "report.target", Node( iPlayer ? target : std::string() ) );
	ui.Set( "report.target_name", Node( name ) );
	CVoiceStatus *pVoice = GetClientVoiceMgr();
	ui.Set( "report.can_mute", Node( iPlayer && pVoice ) );
	ui.Set( "report.muted", Node( iPlayer && pVoice && pVoice->IsPlayerBlocked( iPlayer ) ) );
}

void ReportResetDetails( fui::Runtime &ui )
{
	Node d;
	for ( const ReportDetail_t &det : k_ReportDetails )
		d[ det.pszID ] = false;
	ui.Set( "report.d", std::move( d ) );
}

bool FuiReportCommand( fui::Runtime &ui, const std::string &name, const std::string &arg, const std::string &arg2 )
{
	Social_t &s = S();
	if ( name == "open_report" )
	{
		// From the scoreboard or the console (fr_report) the menu may not be up.
		if ( !enginevgui->IsGameUIVisible() )
			engine->ClientCmd_Unrestricted( "gameui_activate\n" );
		ui.Set( "ui.modal", Node( "report" ) );
		if ( s.flReportDeadline > 0. )
			return true;

		const CSteamID me = Me();
		const CSteamID lastKiller = FrontressReport_LastKiller();
		const int iLocal = engine->GetLocalPlayer();
		const int iMyTeam = g_PR ? g_PR->GetTeam( iLocal ) : TEAM_UNASSIGNED;
		struct Row_t { Node node; int nKills; bool bEnemy; std::string name; };
		std::vector< Row_t > rows;
		for ( int i = 1; i <= gpGlobals->maxClients; ++i )
		{
			player_info_t info;
			if ( !engine->GetPlayerInfo( i, &info ) || !info.friendsID || info.fakeplayer )
				continue;
			const CSteamID id( info.friendsID, me.GetEUniverse(), k_EAccountTypeIndividual );
			if ( id == me )
				continue;
			Row_t row;
			const int iTeam = g_PR ? g_PR->GetTeam( i ) : TEAM_UNASSIGNED;
			row.nKills = FrontressReport_KillsBy( id );
			row.bEnemy = iTeam >= FIRST_GAME_TEAM && iMyTeam >= FIRST_GAME_TEAM && iTeam != iMyTeam;
			row.name = info.name;
			Node &p = row.node;
			p[ "id" ] = std::to_string( id.ConvertToUint64() );
			p[ "name" ] = info.name;
			p[ "team" ] = iTeam == TF_TEAM_RED ? "red" : iTeam == TF_TEAM_BLUE ? "blu" : "spec";
			p[ "cls" ] = g_TF_PR ? ReportClassID( g_TF_PR->GetPlayerClass( i ) ) : "";
			p[ "score" ] = g_TF_PR ? g_TF_PR->GetTotalScore( i ) : 0;
			p[ "kills" ] = row.nKills;
			ISteamFriends *pFriends = steamapicontext ? steamapicontext->SteamFriends() : NULL;
			p[ "avatar" ] = ( pFriends && pFriends->GetLargeFriendAvatar( id ) > 0 ) ? "avatar:" + std::to_string( id.ConvertToUint64() ) : std::string();
			p[ "last_killer" ] = lastKiller.IsValid() && id == lastKiller;
			p[ "reported" ] = !CanReportPlayer( id, false );
			rows.push_back( std::move( row ) );
		}
		// Who killed you first, then the other team, then yours.
		std::stable_sort( rows.begin(), rows.end(), []( const Row_t &a, const Row_t &b ) {
			if ( a.nKills != b.nKills )
				return a.nKills > b.nKills;
			if ( a.bEnemy != b.bEnemy )
				return a.bEnemy;
			return V_stricmp( a.name.c_str(), b.name.c_str() ) < 0;
		} );
		Node players;
		players.SetArray();
		for ( Row_t &row : rows )
			players.Append() = std::move( row.node );
		ui.Set( "report.players", std::move( players ) );

		const int nSource = V_atoi( arg2.c_str() );
		s.nReportSource = CMsgGC_ReportPlayer_ESource_IsValid( nSource ) ? nSource : CMsgGC_ReportPlayer_ESource_kSource_MENU;
		ReportSelect( ui, Digits( arg ) );
		ui.Set( "report.reason", Node( 0 ) );
		ui.Set( "report.comment", Node( "" ) );
		ui.Set( "report.sent", Node( false ) );
		ui.Set( "report.pending", Node( false ) );
		ui.Set( "report.eac", Node( FrontressAC_Client_CanReport() ) );
		ReportResetDetails( ui );
		return true;
	}
	if ( name == "report_pick" )
	{
		if ( s.flReportDeadline <= 0. )
			ReportSelect( ui, Digits( arg ) );
		return true;
	}
	if ( name == "report_reason" )
	{
		const int nReason = V_atoi( arg.c_str() );
		if ( nReason > CMsgGC_ReportPlayer_EReason_kReason_INVALID && nReason < CMsgGC_ReportPlayer_EReason_kReason_COUNT )
		{
			ui.Set( "report.reason", Node( nReason ) );
			ReportResetDetails( ui );
		}
		return true;
	}
	if ( name == "report_detail" )
	{
		for ( const ReportDetail_t &det : k_ReportDetails )
		{
			if ( arg == det.pszID )
			{
				const std::string path = std::string( "report.d." ) + det.pszID;
				const Node *on = ui.Get( path.c_str() );
				ui.Set( path.c_str(), Node( !( on && on->AsBool() ) ) );
			}
		}
		return true;
	}
	if ( name == "send_report" )
	{
		if ( s.flReportDeadline > 0. )
			return true;
		const std::string target = Path( ui, "report.target" );
		const CSteamID id( V_atoui64( target.c_str() ) );
		const Node *reason = ui.Get( "report.reason" );
		const int nReason = reason ? (int)reason->AsNumber() : 0;
		uint32 unDetail = 0;
		for ( const ReportDetail_t &det : k_ReportDetails )
		{
			const Node *on = ui.Get( ( std::string( "report.d." ) + det.pszID ).c_str() );
			if ( det.nReason == nReason && on && on->AsBool() )
				unDetail |= det.unBit;
		}
		if ( !FrontressGC() || !FrontressGC()->BConnected() )
			FuiTF().Toast( "error", ui.Translate( "fui_err_title" ), ui.Translate( "fui_gc_offline" ), "" );
		else if ( ReportPlayerAccountEx( id, nReason, unDetail, Path( ui, "report.comment" ).c_str(), s.nReportSource ) )
		{
			s.reportTarget = target;
			s.flReportDeadline = Plat_FloatTime() + 15.;
			ui.Set( "report.pending", Node( true ) );
		}
		else
			FuiTF().Toast( "error", ui.Translate( "fui_err_title" ), ui.Translate( "fui_report_failed" ), "" );
		return true;
	}
	if ( name == "report_mute" )
	{
		const int iPlayer = ReportPlayerIndex( CSteamID( V_atoui64( Path( ui, "report.target" ).c_str() ) ) );
		if ( CVoiceStatus *pVoice = GetClientVoiceMgr() )
		{
			if ( iPlayer )
			{
				pVoice->SetPlayerBlockedState( iPlayer, !pVoice->IsPlayerBlocked( iPlayer ) );
				ui.Set( "report.muted", Node( pVoice->IsPlayerBlocked( iPlayer ) ) );
			}
		}
		return true;
	}
	if ( name == "report_done" )
	{
		ui.Set( "ui.modal", Node( "" ) );
		ui.Set( "report.sent", Node( false ) );
		return true;
	}
	return false;
}

} // namespace

//-----------------------------------------------------------------------------
void FuiOnSocialEvent( const char *pszJSON )
{
	if ( pszJSON && *pszJSON && S().events.size() < 512 )
		S().events.push_back( pszJSON );
}

void FuiSocialShutdown()
{
	delete S().pTxn;	// unregisters the Steam callback
	S().pTxn = nullptr;
	S().events.clear();
}

const std::string &FuiSocialMyTag() { return S().myTag; }
bool FuiSocialMySupporter() { return S().bMeSupporter; }

void FuiSocialUpdate( fui::Runtime &ui, bool bShown )
{
	Social_t &s = S();
	if ( !s.pTxn && steamapicontext && steamapicontext->SteamUser() )
		s.pTxn = new CMicroTxnListener;

	// Say hello whenever the connection comes (back): the coordinator answers
	// with the chat so far, the online list, the clan, the badge.
	const bool bConnected = FrontressGC() && FrontressGC()->BConnected();
	if ( bConnected && !s.bConnected )
		s.bHelloSent = false;
	s.bConnected = bConnected;
	if ( bConnected && !s.bHelloSent )
	{
		Node hello = Op( "hello" );
		hello[ "name" ] = UTF8Name( Me() );
		const char *pszLang = ( steamapicontext && steamapicontext->SteamApps() ) ? steamapicontext->SteamApps()->GetCurrentGameLanguage() : "english";
		hello[ "lang" ] = pszLang ? pszLang : "english";
		s.bHelloSent = Send( hello );
	}

	// Steam's answers to purchases go straight back to the coordinator.
	if ( s.pTxn )
	{
		auto &answers = s.pTxn->m_Answers;
		for ( auto it = answers.begin(); it != answers.end(); )
		{
			const auto &answer = *it;
			Node op = Op( "support_auth" );
			op[ "order" ] = std::to_string( answer.first );
			op[ "ok" ] = answer.second;
			if ( Send( op ) )
				it = answers.erase( it );
			else
				++it;
		}
	}

	std::vector< std::string > events;
	events.swap( s.events );
	for ( const std::string &json : events )
		ApplyEvent( ui, json );
	if ( s.flReportDeadline > 0. && Plat_FloatTime() >= s.flReportDeadline )
	{
		s.flReportDeadline = 0.;
		ui.Set( "report.pending", Node( false ) );
		FuiTF().Toast( "error", ui.Translate( "fui_report_title" ), ui.Translate( "fui_report_timeout" ), "" );
	}

	if ( s.flSupportDeadline > 0. && Plat_FloatTime() >= s.flSupportDeadline )
	{
		ReportOverlay( s.pendingOrder.empty() ? "open_timeout" : "authorization_timeout" );
		SupportError( ui, "timeout" );
	}
	if ( bShown )
		RefreshInvites( ui, Path( ui, "ui.modal" ) == "invite" );

	const std::string tab = Path( ui, "social.tab" );
	if ( !tab.empty() && tab != s.tab )
	{
		s.tab = tab;
		if ( tab == "clan" )
			s.nUnreadClan = 0;
		if ( tab == "party" )
			s.nUnreadParty = 0;
		s.bViewDirty = true;
	}
	if ( bShown )
		RefreshView( ui );
}

void FuiSocialNavigate( fui::Runtime &ui, const std::string &screen )
{
	if ( screen == "clan" )
		Send( Op( "clan_browse" ) );
	if ( screen == "support" )
	{
		ui.Set( "support.loading", Node( true ) );
		if ( !Send( Op( "support_info" ) ) )
		{
			ui.Set( "support.loading", Node( false ) );
			SupportError( ui, "unreachable" );
		}
	}
}

bool FuiSocialCommand( fui::Runtime &ui, const std::string &name, const std::vector< std::string > &args )
{
	const std::string arg = args.empty() ? std::string() : args[ 0 ];
	const std::string arg2 = args.size() > 1 ? args[ 1 ] : std::string();

	// The chat box's change event: on Enter, send.
	if ( name == "chat_input" )
	{
		if ( arg == "1" || arg == "true" )
			FuiSocialCommand( ui, "chat_send", { arg2 } );
		return true;
	}
	if ( name == "chat_send" )
	{
		std::string text = arg.empty() ? Path( ui, "ui.chat_draft" ) : arg;
		while ( !text.empty() && ( text.back() == '\n' || text.back() == '\r' ) )
			text.pop_back();
		if ( text.find_first_not_of( " \t" ) == std::string::npos )
			return true;
		Node op = Op( "say" );
		op[ "ch" ] = S().tab;
		op[ "text" ] = text;
		if ( Send( op ) )
			ui.Set( "ui.chat_draft", Node( "" ) );
		return true;
	}
	if ( name == "lfg_toggle" )
	{
		const bool bOn = !( ui.Get( "social.lfg" ) && ui.Get( "social.lfg" )->AsBool() );
		Node op = Op( "lfg" );
		op[ "on" ] = bOn;
		op[ "note" ] = Path( ui, "social.lfg_note" );
		if ( Send( op ) )
			ui.Set( "social.lfg", Node( bOn ) );
		return true;
	}
	if ( name == "open_invites" )
	{
		RefreshInvites( ui, true );
		ui.Set( "ui.modal", Node( "invite" ) );
		return true;
	}
	if ( name == "leave_party" )
	{
		auto *pParty = GTFPartyClient();
		if ( pParty && pParty->GetActiveParty() )
		{
			ui.Set( "ui.party_target", Node( std::to_string( pParty->GetActiveParty()->GetGroupID() ) ) );
			ui.Set( "ui.modal", Node( "leave_party" ) );
		}
		return true;
	}
	if ( name == "confirm_leave_party" )
	{
		auto *pParty = GTFPartyClient();
		if ( FrontressGC() && FrontressGC()->BConnected() && pParty && pParty->GetActiveParty() &&
			Path( ui, "ui.party_target" ) == std::to_string( pParty->GetActiveParty()->GetGroupID() ) )
		{
			pParty->LeaveActiveParty();
			ui.Set( "ui.modal", Node( "" ) );
		}
		else
			FuiTF().Toast( "error", ui.Translate( "fui_err_title" ), ui.Translate( "fui_gc_offline" ), "" );
		return true;
	}
	if ( name == "open_report" || name == "report_pick" || name == "report_reason" || name == "report_detail" ||
		name == "send_report" || name == "report_mute" || name == "report_done" )
		return FuiReportCommand( ui, name, arg, arg2 );
	if ( name == "invite_player" || name == "join_player" || name == "accept_invite" || name == "decline_invite" ||
		name == "cancel_invite" || name == "accept_request" || name == "kick_member" || name == "confirm_kick" || name == "profile" )
	{
		const uint64 ul = V_atoui64( Digits( arg ).c_str() );
		CSteamID id( ul );
		if ( !id.IsValid() || !id.BIndividualAccount() || id == Me() )
			return true;
		if ( name == "profile" )
		{
			if ( steamapicontext && steamapicontext->SteamFriends() )
				steamapicontext->SteamFriends()->ActivateGameOverlayToUser( "steamid", id );
		}
		else if ( FrontressGC() && FrontressGC()->BConnected() && GTFPartyClient() )
		{
			auto *pParty = GTFPartyClient();
			if ( name == "kick_member" )
			{
				if ( pParty->BIsPartyLeader() && pParty->GetActiveParty() && pParty->GetActiveParty()->GetMemberIndexBySteamID( id ) >= 0 )
				{
					ui.Set( "ui.kick_target", Node( arg ) );
					ui.Set( "ui.kick_name", Node( UTF8Name( id ) ) );
					ui.Set( "ui.party_target", Node( std::to_string( pParty->GetActiveParty()->GetGroupID() ) ) );
					ui.Set( "ui.modal", Node( "kick_member" ) );
				}
			}
			else if ( name == "confirm_kick" )
			{
				if ( pParty->GetActiveParty() && Path( ui, "ui.party_target" ) == std::to_string( pParty->GetActiveParty()->GetGroupID() ) &&
					pParty->BKickPartyMember( id ) )
					ui.Set( "ui.modal", Node( "invite" ) );
			}
			else if ( name == "cancel_invite" )
				pParty->CancelOutgoingInviteOrIncomingJoinRequest( id );
			else if ( name == "accept_request" )
				pParty->BInvitePlayerToParty( id, true );
			else if ( name == "decline_invite" )
				pParty->CancelOutgoingJoinRequestOrIncomingInvite( id );
			else
			{
				const bool bInvite = name == "invite_player";
				const bool bSent = bInvite ? pParty->BInvitePlayerToParty( id, false ) : pParty->BRequestJoinPlayer( id, name == "accept_invite" );
				if ( bSent )
				{
					FuiTF().Toast( "party", ui.Translate( bInvite ? "fui_toast_invited" : "fui_toast_asked" ), UTF8Name( id ), "" );
					if ( name == "accept_invite" )
						ui.Set( "ui.modal", Node( "" ) );
				}
				else
					FuiTF().Toast( "error", ui.Translate( "fui_err_title" ), ui.Translate( "fui_invite_failed" ), "" );
			}
		}
		else
			FuiTF().Toast( "error", ui.Translate( "fui_err_title" ), ui.Translate( "fui_gc_offline" ), "" );
		return true;
	}

	// Clans
	if ( name == "clan_create" )
	{
		Node op = Op( "clan_create" );
		op[ "name" ] = Path( ui, "clanform.name" );
		op[ "tag" ] = Path( ui, "clanform.tag" );
		op[ "motto" ] = Path( ui, "clanform.motto" );
		op[ "emblem" ] = Path( ui, "clanform.emblem" );
		op[ "color" ] = Path( ui, "clanform.color" );
		op[ "open" ] = ui.Get( "clanform.open" ) && ui.Get( "clanform.open" )->AsBool();
		Send( op );
		return true;
	}
	if ( name == "clan_settings" )
	{
		Node op = Op( "clan_settings" );
		op[ "motto" ] = Path( ui, "clanform.motto" );
		op[ "emblem" ] = Path( ui, "clanform.emblem" );
		op[ "color" ] = Path( ui, "clanform.color" );
		op[ "open" ] = ui.Get( "clanform.open" ) && ui.Get( "clanform.open" )->AsBool();
		Send( op );
		ui.Set( "ui.modal", Node( "" ) );
		return true;
	}
	if ( name == "clan_join" || name == "clan_decline" )
	{
		Node op = Op( name.c_str() );
		op[ "id" ] = Word( arg );
		Send( op );
		return true;
	}
	if ( name == "clan_leave" || name == "clan_browse" )
	{
		Send( Op( name.c_str() ) );
		ui.Set( "ui.modal", Node( "" ) );
		return true;
	}
	if ( name == "clan_invite" || name == "clan_kick" )
	{
		Node op = Op( name.c_str() );
		op[ "who" ] = Digits( arg );
		Send( op );
		return true;
	}
	if ( name == "clan_role" )
	{
		Node op = Op( "clan_role" );
		op[ "who" ] = Digits( arg );
		op[ "role" ] = Word( arg2 );
		Send( op );
		return true;
	}
	if ( name == "clan_order" )
	{
		Node op = Op( "clan_order" );
		op[ "front" ] = Word( arg );
		Send( op );
		return true;
	}

	// Support
	if ( name == "support_buy" )
	{
		if ( ui.Get( "support.busy" ) && ui.Get( "support.busy" )->AsBool() )
			return true;
		auto *pUtils = steamapicontext ? steamapicontext->SteamUtils() : nullptr;
		if ( !Bool( ui.Get( "support" ), "simulated" ) && ( !pUtils || !pUtils->IsOverlayEnabled() ) )
		{
			ReportOverlay( "unavailable" );
			SupportError( ui, "overlay" );
			return true;
		}
		Node op = Op( "support_buy" );
		op[ "kind" ] = arg == "monthly" ? "monthly" : "once";
		op[ "tier" ] = atoi( arg2.c_str() );
		op[ "anon" ] = ui.Get( "support.anon" ) && ui.Get( "support.anon" )->AsBool();
		if ( Send( op ) )
		{
			ui.Set( "support.busy", Node( true ) );
			ui.Set( "support.result", Node( "opening" ) );
			S().flSupportDeadline = Plat_FloatTime() + 35.;
		}
		else
			SupportError( ui, "unreachable" );
		return true;
	}
	if ( name == "support_cancel" )
	{
		Send( Op( "support_cancel" ) );
		ui.Set( "ui.modal", Node( "" ) );
		return true;
	}
	if ( name == "support_refresh" )
	{
		Send( Op( "support_info" ) );
		return true;
	}
	return false;
}

//-----------------------------------------------------------------------------
// fr_report [steamid64 | killer]: the report dialog, for binds and the
// scoreboard. "killer" picks whoever last killed you.
//-----------------------------------------------------------------------------
CON_COMMAND( fr_report, "Report a player: fr_report [steamid64 | killer]. Bind it: bind F7 \"fr_report killer\"." )
{
	if ( !FrontressUI() )
		return;
	std::string target, source = std::to_string( CMsgGC_ReportPlayer_ESource_kSource_MENU );
	if ( args.ArgC() > 1 && !V_stricmp( args[ 1 ], "killer" ) )
	{
		const CSteamID killer = FrontressReport_LastKiller();
		if ( killer.IsValid() )
			target = std::to_string( killer.ConvertToUint64() );
		source = std::to_string( CMsgGC_ReportPlayer_ESource_kSource_KILLER );
	}
	else if ( args.ArgC() > 1 )
	{
		target = Digits( args[ 1 ] );
		if ( args.ArgC() > 2 )
			source = Digits( args[ 2 ] );
	}
	FuiTF().OnCommand( "open_report", { target, source } );
}
