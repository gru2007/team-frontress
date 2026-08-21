//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: the two questions the main menu's war map page asks that only the
// engine can answer — is this client in a battle yet (for the connecting and
// result UI), and has this listen server's own advertised address arrived yet
// (for standing up a P2P battle; see greyline_hoststate.cpp, the only thing
// that ever sets the convars read here).
//
// They are registered as ordinary methods on the game's existing web RPC —
// game/shared/gamestate — because there is exactly one loopback server in
// this process and that is it: the same server that hands the CEF panel the
// page over http://127.0.0.1:58270/ui/greyline.html and already answers the
// page's other three calls, "cmd", "getcvar" and "fetch".
//
// This used to be a second, hand-rolled websocket server bound to that very
// same port. Two listeners on one port is a race, and the platforms do not
// even agree on who wins it: Linux refuses the second bind outright (so the
// bridge quietly never ran and everything worked), while Winsock's
// SO_REUSEADDR lets the later bind steal the port. Under Wine the bridge won,
// so the panel's plain GET for the page itself landed on a socket that only
// speaks RFC 6455, found no Sec-WebSocket-Key, and closed without writing a
// byte — which CEF renders as net::ERR_EMPTY_RESPONSE, -324.
//
//=========================================================================//

#include "cbase.h"
#include "gamestate/gamestate.h"
#include "GameEventListener.h"
#include "clientmode_tf.h"
#include "hud.h"
#include "hudelement.h"
#include "hud_chat.h"
#include "inetchannelinfo.h"
#include "tier1/fmtstr.h"
#include "steam/steam_api.h"

#include <string>

// How many times this client has watched a battle end. The page polls it to
// tell "the match I was in just finished" apart from "I am not in a match" —
// both of which look like in_game:false a moment later. File scope so the
// method lambdas below, which take no captures, can read it.
static int g_nGreylineGameOverSeq = 0;

// The identity Steam binds the coordinator's auth tickets to. It has to be the
// same string on both sides — auth.identity in the coordinator config —
// because Steam validates the ticket against it and a mismatch is refused
// exactly like a forgery. See docs/STEAM-SETUP.md.
static const char *const kGreylineTicketIdentity = "greyline";

// How long a ticket is reused before a fresh one is asked for. Steam expires
// them on its own schedule and an expired ticket is refused like a bad one, so
// this is deliberately far shorter than any lifetime Valve documents.
static const float kGreylineTicketMaxAge = 300.0f;

static bool RunMenuAction( const std::string &action )
{
	if ( !GetClientModeTFNormal() || !GetClientModeTFNormal()->GameUI() )
	{
		return false;
	}

	const char *pszCommand = NULL;
	if ( action == "settings" )
		pszCommand = "engine opentf2options";
	else if ( action == "legacy_settings" )
		pszCommand = "OpenOptionsDialog";
	else if ( action == "loadout" )
		pszCommand = "engine open_charinfo";
	else if ( action == "armory" )
		pszCommand = "engine open_charinfo_armory";
	else if ( action == "servers" )
		pszCommand = "OpenServerBrowser";
	else if ( action == "resume" )
		pszCommand = "ResumeGame";
	else if ( action == "disconnect" )
		pszCommand = "engine disconnect";
	else if ( action == "quit" )
		pszCommand = "Quit";

	if ( !pszCommand )
	{
		return false;
	}
	GetClientModeTFNormal()->GameUI()->SendMainMenuCommand( pszCommand );
	return true;
}

// CountConnectedPlayers is how many people are actually on this server right
// now. When this client is hosting a battle it is the number the coordinator
// puts on the war map, and it is the only one worth putting there: the roster
// says how many were sent, which is a different — and, while people are still
// loading in, a misleading — number.
static int CountConnectedPlayers()
{
	int nPlayers = 0;
	const int nMaxClients = engine->GetMaxClients();
	for ( int i = 1; i <= nMaxClients; ++i )
	{
		player_info_t info;
		if ( engine->GetPlayerInfo( i, &info ) && !info.fakeplayer )
		{
			++nPlayers;
		}
	}
	return nPlayers;
}

//-----------------------------------------------------------------------------
// Purpose: counts game endings for the page and hangs the war map's two
// engine queries off the web RPC once that system exists.
//-----------------------------------------------------------------------------
class CGreylineMenuRPC : public CAutoGameSystem, public CGameEventListener
{
public:
	CGreylineMenuRPC()
		: CAutoGameSystem( "CGreylineMenuRPC" ),
		  m_CallbackWebApiTicket( this, &CGreylineMenuRPC::OnWebApiTicket )
	{
		m_hAuthTicket = k_HAuthTicketInvalid;
		m_cbAuthTicket = 0;
		m_bTicketPending = false;
		m_flTicketIssuedAt = 0.0f;
	}

	virtual bool Init() OVERRIDE
	{
		ListenForGameEvent( "teamplay_game_over" );
		ListenForGameEvent( "tf_game_over" );
		return true;
	}

	// PostInit, not Init: CGameStateManager::RegisterMethod silently does
	// nothing before that system's own Init has run, and the order two auto
	// game systems get initialized in is not something either of them decides.
	// Every Init runs before every PostInit, so by here the RPC is there.
	virtual void PostInit() OVERRIDE
	{
		CGameStateManager *pManager = GetGameStateManager();
		if ( !pManager )
		{
			return;
		}

		pManager->RegisterMethod( "greyline_state", std::function( []( const std::string &params, int64_t iRpcId )
		{
			const bool bInGame = engine->IsInGame();
			const bool bBackground = bInGame && engine->IsLevelMainMenuBackground();
			const bool bConnected = bInGame && !bBackground;
			// A connection this client is still in the middle of making. The
			// handshake, the map download and the map load all live here, and
			// all three of them take longer than any sane retry interval: the
			// page has to be able to tell "this is going nowhere" apart from
			// "this is working", because issuing `connect` again during any of
			// them throws the attempt away and starts it over — forever.
			const bool bLoading = engine->IsDrawingLoadingImage();
			const bool bConnecting = ( engine->IsConnected() && !bInGame ) || bLoading;
			INetChannelInfo *pChannel = engine->GetNetChannelInfo();
			const char *pszServer = pChannel ? pChannel->GetAddress() : NULL;
			const char *pszLevel = bInGame ? engine->GetLevelName() : NULL;
			CFmtStr1024 strJSON( "{\"in_game\":%s,\"connected\":%s,\"background\":%s,"
				"\"connecting\":%s,\"loading\":%s,\"server\":\"%s\","
				"\"level\":\"%s\",\"players\":%d,\"game_over_seq\":%d}",
				bInGame ? "true" : "false",
				bConnected ? "true" : "false",
				bBackground ? "true" : "false",
				bConnecting ? "true" : "false",
				bLoading ? "true" : "false",
				pszServer ? pszServer : "",
				pszLevel ? pszLevel : "",
				bConnected ? CountConnectedPlayers() : 0,
				g_nGreylineGameOverSeq );
			return std::make_pair( true, std::string( strJSON.Get() ) );
		} ) );

		pManager->RegisterMethod( "greyline_identity", std::function( [this]( const std::string &params, int64_t iRpcId )
		{
			return std::make_pair( true, BuildIdentityJSON() );
		} ) );

		pManager->RegisterMethod( "greyline_menu_action", std::function( []( const std::string &params, int64_t iRpcId )
		{
			if ( !RunMenuAction( params ) )
			{
				return std::make_pair( true, std::string( "{\"ok\":false,\"error\":\"unknown or unavailable menu action\"}" ) );
			}
			return std::make_pair( true, std::string( "{\"ok\":true}" ) );
		} ) );

		pManager->RegisterMethod( "greyline_integrity", std::function( []( const std::string &params, int64_t iRpcId )
		{
			ConVarRef tainted( "greyline_policy_tainted", true );
			ConVarRef violation( "greyline_policy_violation", true );
			const bool bTainted = tainted.IsValid() && tainted.GetBool();
			CFmtStr1024 strJSON( "{\"ok\":%s,\"tainted\":%s,\"violation\":\"%s\"}",
				bTainted ? "false" : "true", bTainted ? "true" : "false",
				violation.IsValid() ? violation.GetString() : "" );
			return std::make_pair( true, std::string( strJSON.Get() ) );
		} ) );

		pManager->RegisterMethod( "greyline_operation_result", std::function( []( const std::string &params, int64_t iRpcId )
		{
			std::string message = params.substr( 0, 480 );
			for ( size_t i = 0; i < message.size(); ++i )
			{
				if ( message[i] == '\r' || message[i] == '\n' )
					message[i] = ' ';
			}
			CBaseHudChat *pHudChat = (CBaseHudChat *)GET_HUDELEMENT( CHudChat );
			if ( !pHudChat )
			{
				return std::make_pair( true, std::string( "{\"ok\":false}" ) );
			}
			pHudChat->ChatPrintf( 0, CHAT_FILTER_NONE, "%c[GREYLINE] %s", COLOR_NORMAL, message.c_str() );
			return std::make_pair( true, std::string( "{\"ok\":true}" ) );
		} ) );

		pManager->RegisterMethod( "greyline_host_address", std::function( []( const std::string &params, int64_t iRpcId )
		{
			// Ignore-missing: both live in the server dll, which is not
			// loaded at all until this client hosts a map.
			ConVarRef addr( "greyline_server_addr", true );
			ConVarRef id( "greyline_server_id", true );
			const bool bIdentityReady = id.IsValid() && id.GetString()[0] && V_strcmp( id.GetString(), "0" ) != 0;
			CFmtStr1024 strJSON( "{\"ready\":%s,\"address\":\"%s\",\"steam_id\":\"%s\"}",
				bIdentityReady ? "true" : "false",
				addr.IsValid() ? addr.GetString() : "",
				id.IsValid() ? id.GetString() : "0" );
			return std::make_pair( true, std::string( strJSON.Get() ) );
		} ) );
	}

	virtual void Shutdown() OVERRIDE
	{
		StopListeningForAllEvents();
		CancelTicket();

		CGameStateManager *pManager = GetGameStateManager();
		if ( pManager )
		{
			pManager->UnregisterMethod( "greyline_state" );
			pManager->UnregisterMethod( "greyline_identity" );
			pManager->UnregisterMethod( "greyline_menu_action" );
			pManager->UnregisterMethod( "greyline_integrity" );
			pManager->UnregisterMethod( "greyline_operation_result" );
			pManager->UnregisterMethod( "greyline_host_address" );
		}
	}

	virtual void FireGameEvent( IGameEvent *pEvent ) OVERRIDE
	{
		if ( !pEvent )
		{
			return;
		}
		const char *pszName = pEvent->GetName();
		if ( !V_strcmp( pszName, "teamplay_game_over" ) || !V_strcmp( pszName, "tf_game_over" ) )
		{
			++g_nGreylineGameOverSeq;
		}
	}

private:
	// OnWebApiTicket is where the ticket actually arrives. GetAuthTicketForWebApi
	// is asynchronous — unlike GetAuthSessionTicket, whose bytes are filled in
	// before it returns — so the page asks for the identity, gets ticket_pending
	// back, and asks again a moment later.
	STEAM_CALLBACK( CGreylineMenuRPC, OnWebApiTicket, GetTicketForWebApiResponse_t, m_CallbackWebApiTicket );

	// RequestTicket asks Steam for a ticket the coordinator can validate.
	//
	// It must be GetAuthTicketForWebApi and not GetAuthSessionTicket: Steam's
	// own header says a session ticket is "not to be used for
	// ISteamUserAuth\AuthenticateUserTicket - it will fail", and it fails with
	// the same rejection a forged ticket gets, so getting this wrong looks from
	// the coordinator's side exactly like somebody attacking it.
	void RequestTicket()
	{
		if ( m_bTicketPending || !steamapicontext || !steamapicontext->SteamUser() )
		{
			return;
		}
		CancelTicket();

		m_hAuthTicket = steamapicontext->SteamUser()->GetAuthTicketForWebApi( kGreylineTicketIdentity );
		if ( m_hAuthTicket == k_HAuthTicketInvalid )
		{
			Warning( "[greyline] Steam refused to issue a web API auth ticket\n" );
			return;
		}
		m_bTicketPending = true;
	}

	void CancelTicket()
	{
		if ( m_hAuthTicket != k_HAuthTicketInvalid && steamapicontext && steamapicontext->SteamUser() )
		{
			steamapicontext->SteamUser()->CancelAuthTicket( m_hAuthTicket );
		}
		m_hAuthTicket = k_HAuthTicketInvalid;
		m_cbAuthTicket = 0;
		m_bTicketPending = false;
		m_flTicketIssuedAt = 0.0f;
	}

	// A ticket goes stale, and a stale one is refused exactly like a bad one.
	// Rather than work out Steam's exact lifetime, this asks for a fresh ticket
	// whenever the page asks for an identity and the one in hand is older than
	// a few minutes — which in practice means once, right before CONNECT.
	bool TicketIsStale() const
	{
		return m_cbAuthTicket == 0 ||
			( Plat_FloatTime() - m_flTicketIssuedAt ) > kGreylineTicketMaxAge;
	}

	std::string BuildIdentityJSON()
	{
		if ( !steamapicontext || !steamapicontext->SteamUser() || !steamapicontext->SteamUser()->BLoggedOn() )
		{
			return "{\"available\":false,\"logged_on\":false,\"steam_id\":\"\",\"ticket\":\"\",\"ticket_pending\":false}";
		}

		const CSteamID steamID = steamapicontext->SteamUser()->GetSteamID();
		if ( !steamID.IsValid() )
		{
			return "{\"available\":false,\"logged_on\":true,\"steam_id\":\"\",\"ticket\":\"\",\"ticket_pending\":false}";
		}

		if ( TicketIsStale() )
		{
			RequestTicket();
		}

		char szSteamID[32];
		V_snprintf( szSteamID, sizeof( szSteamID ), "%llu", steamID.ConvertToUint64() );
		static const char s_Hex[] = "0123456789abcdef";
		std::string ticket;
		ticket.reserve( m_cbAuthTicket * 2 );
		for ( uint32 i = 0; i < m_cbAuthTicket; ++i )
		{
			ticket.push_back( s_Hex[( m_AuthTicket[i] >> 4 ) & 0x0f] );
			ticket.push_back( s_Hex[m_AuthTicket[i] & 0x0f] );
		}

		// The SteamID is available immediately and the ticket is not, so the
		// identity is "available" either way and ticket_pending is what tells
		// the page to ask again before it connects. Under auth.mode=dev the
		// coordinator never looks at the ticket, and this all costs nothing.
		std::string json = "{\"available\":true,\"logged_on\":true,\"steam_id\":\"";
		json += szSteamID;
		json += "\",\"ticket\":\"";
		json += ticket;
		json += "\",\"ticket_pending\":";
		json += m_bTicketPending ? "true" : "false";
		json += "}";
		return json;
	}

	HAuthTicket m_hAuthTicket;
	uint32 m_cbAuthTicket;
	bool m_bTicketPending;
	float m_flTicketIssuedAt;
	uint8 m_AuthTicket[GetTicketForWebApiResponse_t::k_nCubTicketMaxLength];
};

//-----------------------------------------------------------------------------
void CGreylineMenuRPC::OnWebApiTicket( GetTicketForWebApiResponse_t *pResponse )
{
	if ( !pResponse || pResponse->m_hAuthTicket != m_hAuthTicket )
	{
		// A response for a ticket we already cancelled. Ignoring it is right:
		// adopting it would hand the coordinator a ticket we asked for under a
		// different identity.
		return;
	}

	m_bTicketPending = false;
	if ( pResponse->m_eResult != k_EResultOK )
	{
		Warning( "[greyline] Steam could not issue a web API auth ticket (EResult %d). "
			"The coordinator will refuse this client unless it is running auth_mode=dev.\n",
			(int)pResponse->m_eResult );
		m_hAuthTicket = k_HAuthTicketInvalid;
		m_cbAuthTicket = 0;
		return;
	}

	const int cbTicket = MIN( pResponse->m_cubTicket, (int)sizeof( m_AuthTicket ) );
	if ( cbTicket <= 0 )
	{
		m_cbAuthTicket = 0;
		return;
	}
	V_memcpy( m_AuthTicket, pResponse->m_rgubTicket, cbTicket );
	m_cbAuthTicket = (uint32)cbTicket;
	m_flTicketIssuedAt = Plat_FloatTime();
}

static CGreylineMenuRPC g_GreylineMenuRPC;
