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
#include "tier1/fmtstr.h"

#include <string>

// How many times this client has watched a battle end. The page polls it to
// tell "the match I was in just finished" apart from "I am not in a match" —
// both of which look like in_game:false a moment later. File scope so the
// method lambdas below, which take no captures, can read it.
static int g_nGreylineGameOverSeq = 0;

//-----------------------------------------------------------------------------
// Purpose: counts game endings for the page and hangs the war map's two
// engine queries off the web RPC once that system exists.
//-----------------------------------------------------------------------------
class CGreylineMenuRPC : public CAutoGameSystem, public CGameEventListener
{
public:
	CGreylineMenuRPC() : CAutoGameSystem( "CGreylineMenuRPC" )
	{
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
			const char *pszLevel = bInGame ? engine->GetLevelName() : NULL;
			CFmtStr1024 strJSON( "{\"in_game\":%s,\"level\":\"%s\",\"game_over_seq\":%d}",
				bInGame ? "true" : "false",
				pszLevel ? pszLevel : "",
				g_nGreylineGameOverSeq );
			return std::make_pair( true, std::string( strJSON.Get() ) );
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

		CGameStateManager *pManager = GetGameStateManager();
		if ( pManager )
		{
			pManager->UnregisterMethod( "greyline_state" );
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
};

static CGreylineMenuRPC g_GreylineMenuRPC;
