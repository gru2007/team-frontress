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

#include "frontress_gc.h"
#include "tf_gc_client.h"
#include "tf_party.h"
#include "tf_partyclient.h"
#include "steam/steam_api.h"
#include "vgui/ILocalize.h"

#include <time.h>

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
};

Social_t &S()
{
	static Social_t s_Social;
	return s_Social;
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
		doc[ "busy" ] = false;
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
		ui.Set( "support.busy", Node( true ) );
		ui.Set( "support.result", Node( "pending" ) );
	}
	else if ( t == "support_result" )
	{
		s.pendingOrder.clear();
		ui.Set( "support.busy", Node( false ) );
		const bool bOK = Bool( &ev, "ok" );
		const std::string kind = Str( &ev, "kind" );
		ui.Set( "support.result", Node( bOK ? ( kind == "cancelled" ? "cancelled" : "thanks" ) : "error" ) );
		ui.Set( "support.error", Node( Str( &ev, "error" ) ) );
		// On the support screen its result bar says the same.
		if ( bOK && kind != "cancelled" && ui.Screen() != "support" )
			FuiTF().Toast( "support", ui.Translate( "fui_support_thanks_title" ), ui.Translate( kind == "monthly" ? "fui_support_thanks_monthly" : "fui_support_thanks_once" ), "" );
	}
	else if ( t == "error" )
		FuiTF().Toast( "error", ui.Translate( "fui_err_title" ), ui.Translate( Str( &ev, "key" ) ), "" );
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
		for ( const auto &answer : s.pTxn->m_Answers )
		{
			Node op = Op( "support_auth" );
			op[ "order" ] = std::to_string( answer.first );
			op[ "ok" ] = answer.second;
			Send( op );
			if ( !answer.second )
				ui.Set( "support.busy", Node( false ) );
		}
		s.pTxn->m_Answers.clear();
	}

	std::vector< std::string > events;
	events.swap( s.events );
	for ( const std::string &json : events )
		ApplyEvent( ui, json );

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
		ui.Set( "support.result", Node( "" ) );
		Send( Op( "support_info" ) );
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
	if ( name == "invite_player" || name == "join_player" || name == "profile" )
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
		else if ( GTFPartyClient() )
		{
			if ( name == "invite_player" )
				GTFPartyClient()->BInvitePlayerToParty( id, false );
			else
				GTFPartyClient()->BRequestJoinPlayer( id, false );
			FuiTF().Toast( "party", ui.Translate( name == "invite_player" ? "fui_toast_invited" : "fui_toast_asked" ), UTF8Name( id ), "" );
		}
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
		Node op = Op( "support_buy" );
		op[ "kind" ] = arg == "monthly" ? "monthly" : "once";
		op[ "tier" ] = atoi( arg2.c_str() );
		op[ "anon" ] = ui.Get( "support.anon" ) && ui.Get( "support.anon" )->AsBool();
		if ( Send( op ) )
		{
			ui.Set( "support.busy", Node( true ) );
			ui.Set( "support.result", Node( "opening" ) );
		}
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
