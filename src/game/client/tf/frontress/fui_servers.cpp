//========= Team Frontress ====================================================//
//
// Server pings for matchmaking. See fui_servers.h.
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

#include "fui_servers.h"
#include "fui_tf.h"

#include "frontress_gc.h"
#include "tf_gc_client.h"
#include "tf_gcmessages.h"
#include "igamesystem.h"
#include "tier1/netadr.h"
#include "steam/steam_api.h"
#include "steam/isteammatchmaking.h"

#include <map>

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

namespace
{

using fui::Node;

const uint32 k_EMsgGCFrontress_ServerPings = 51022;	// services/gc/internal/gc/probe.go
const double k_flPingDeadline = 10.0;	// Steam gives up on a server well before this

struct Target_t
{
	std::string id, addr, region;
	bool bDone = false, bOK = false;
	int ms = -1;
};

class CServerPing : public ISteamMatchmakingPingResponse
{
public:
	explicit CServerPing( size_t i ) : m_i( i ) {}
	void ServerResponded( gameserveritem_t &server ) OVERRIDE;
	void ServerFailedToRespond() OVERRIDE;

	size_t m_i;
	HServerQuery m_hQuery = HSERVERQUERY_INVALID;
	bool m_bDone = false;
};

struct State_t
{
	std::string pending;	// a list waiting to be measured
	std::vector< Target_t > targets;
	std::vector< CServerPing * > pings;
	std::vector< std::pair< std::string, std::string > > regions;	// id, name
	int maxMS = 200;
	double flDeadline = 0.;
	bool bRunning = false;
	std::string shown;	// the region picker as last published
	std::string reach;	// why the queue stopped (51023), waiting for the menu
};

State_t &S()
{
	static State_t s;
	return s;
}

ISteamMatchmakingServers *Servers()
{
	return steamapicontext ? steamapicontext->SteamMatchmakingServers() : NULL;
}

void Finish( size_t i, bool bOK, int ms )
{
	State_t &s = S();
	if ( i >= s.targets.size() )
		return;
	Target_t &t = s.targets[ i ];
	t.bDone = true;
	t.bOK = bOK;
	t.ms = bOK ? ms : -1;
}

void CServerPing::ServerResponded( gameserveritem_t &server )
{
	m_bDone = true;
	Finish( m_i, true, server.m_nPing );
}

void CServerPing::ServerFailedToRespond()
{
	m_bDone = true;
	Finish( m_i, false, -1 );
}

void CancelAll()
{
	State_t &s = S();
	ISteamMatchmakingServers *pServers = Servers();
	for ( CServerPing *p : s.pings )
	{
		if ( !p->m_bDone && p->m_hQuery != HSERVERQUERY_INVALID && pServers )
			pServers->CancelServerQuery( p->m_hQuery );
		delete p;
	}
	s.pings.clear();
	s.bRunning = false;
}

void Start( const std::string &json )
{
	State_t &s = S();
	CancelAll();
	Node doc;
	if ( !Node::Parse( json, doc ) || !doc.IsObject() )
		return;

	s.targets.clear();
	s.regions.clear();
	s.maxMS = doc.Find( "max_ms" ) ? (int)doc.Find( "max_ms" )->AsNumber( 200 ) : 200;
	if ( const Node *pRegions = doc.Find( "regions" ) )
	{
		for ( size_t i = 0; i < pRegions->Size(); ++i )
		{
			const Node &r = *pRegions->At( i );
			const Node *pID = r.Find( "id" ), *pName = r.Find( "name" );
			if ( pID && pName )
				s.regions.emplace_back( pID->AsString(), pName->AsString() );
		}
	}
	if ( const Node *pServers = doc.Find( "servers" ) )
	{
		for ( size_t i = 0; i < pServers->Size() && i < 64; ++i )
		{
			const Node &srv = *pServers->At( i );
			Target_t t;
			t.id = srv.Find( "id" ) ? srv.Find( "id" )->AsString() : "";
			t.addr = srv.Find( "addr" ) ? srv.Find( "addr" )->AsString() : "";
			t.region = srv.Find( "region" ) ? srv.Find( "region" )->AsString() : "";
			if ( !t.id.empty() && !t.addr.empty() )
				s.targets.push_back( t );
		}
	}

	// Steam answers on its own callbacks; a server it cannot even look up
	// is one this player cannot reach.
	ISteamMatchmakingServers *pServers = Servers();
	for ( size_t i = 0; i < s.targets.size(); ++i )
	{
		netadr_t adr;
		if ( !pServers || !adr.SetFromString( s.targets[ i ].addr.c_str(), true ) || !adr.GetIPHostByteOrder() )
		{
			Finish( i, false, -1 );
			continue;
		}
		CServerPing *pPing = new CServerPing( i );
		s.pings.push_back( pPing );
		const uint16 port = adr.GetPort() ? adr.GetPort() : 27015;
		pPing->m_hQuery = pServers->PingServer( adr.GetIPHostByteOrder(), port, pPing );
	}
	s.flDeadline = Plat_FloatTime() + k_flPingDeadline;
	s.bRunning = true;
}

void Report()
{
	State_t &s = S();
	Node doc;
	Node &pings = doc[ "pings" ];
	pings.SetArray();
	for ( const Target_t &t : s.targets )
	{
		Node &p = pings.Append();
		p[ "id" ] = t.id;
		p[ "ms" ] = t.ms;
		p[ "ok" ] = t.bDone && t.bOK;
		DevMsg( "[frontress] server %s (%s, %s): %s %d ms\n", t.id.c_str(), t.addr.c_str(), t.region.c_str(),
		        t.bDone && t.bOK ? "reached" : "no answer", t.ms );
	}
	if ( FrontressGC() && FrontressGC()->BConnected() && GCClientSystem() )
	{
		GCSDK::CProtoBufMsg< CMsgPartyChatMsg > msg( k_EMsgGCFrontress_ServerPings );
		msg.Body().set_msg( doc.Dump().c_str() );
		GCClientSystem()->BSendMessage( msg );
	}
}

// Runs every frame, menu or not: a queued player in a match still answers.
class CFuiServerPings : public CAutoGameSystemPerFrame
{
public:
	CFuiServerPings() : CAutoGameSystemPerFrame( "CFuiServerPings" ) {}

	void Update( float frametime ) OVERRIDE
	{
		State_t &s = S();
		if ( !s.pending.empty() )
		{
			std::string json;
			json.swap( s.pending );
			Start( json );
		}
		if ( !s.bRunning )
			return;
		bool bAllDone = true;
		for ( const Target_t &t : s.targets )
			bAllDone = bAllDone && t.bDone;
		if ( bAllDone || Plat_FloatTime() >= s.flDeadline )
		{
			Report();
			CancelAll();
		}
	}

	void Shutdown() OVERRIDE { CancelAll(); }
};
CFuiServerPings s_FuiServerPings;

} // namespace

void FuiOnServerList( const char *pszJSON )
{
	if ( pszJSON )
		S().pending = pszJSON;
}

void FuiOnUnreachable( const char *pszJSON )
{
	if ( !pszJSON || !pszJSON[ 0 ] )
		return;
	S().reach = pszJSON;
	Msg( "Game coordinator: no server within the ping limit, searching on the best one there is: %s\n", pszJSON );
}

namespace
{

std::string Arg( std::string text, const char *pszKey, const std::string &value )
{
	const size_t at = text.find( pszKey );
	if ( at != std::string::npos )
		text.replace( at, strlen( pszKey ), value );
	return text;
}

// The coordinator's report (probe.go, reachReport): no server suits the party
// within the ping limit, so it plays on the best one there is. A toast says
// so once; reach { relaxed, why, best, best_ms } keeps it under the searching
// button.
void ShowReach( fui::Runtime &ui, const std::string &json )
{
	Node doc;
	if ( !Node::Parse( json, doc ) || !doc.IsObject() )
		return;
	const bool bSelf = doc.Find( "self" ) && doc.Find( "self" )->AsBool();
	const bool bParty = doc.Find( "party" ) && doc.Find( "party" )->AsBool();
	const std::string maxMS = std::to_string( doc.Find( "max_ms" ) ? (int)doc.Find( "max_ms" )->AsNumber( 200 ) : 200 );
	std::string who;
	if ( const Node *pWho = doc.Find( "who" ) )
	{
		for ( size_t i = 0; i < pWho->Size(); ++i )
		{
			if ( !who.empty() )
				who += ", ";
			who += pWho->At( i )->AsString();
		}
	}
	std::string server, region;
	int ms = -1;
	if ( const Node *pBest = doc.Find( "best" ) )
	{
		server = pBest->Find( "name" ) ? pBest->Find( "name" )->AsString() : "";
		region = pBest->Find( "region" ) ? pBest->Find( "region" )->AsString() : "";
		ms = pBest->Find( "ms" ) ? (int)pBest->Find( "ms" )->AsNumber( -1 ) : -1;
	}
	if ( server.empty() )
		server = region;
	else if ( !region.empty() && region != server )
		server += " (" + region + ")";

	std::string why;
	if ( bSelf )
		why = Arg( ui.Translate( "fui_reach_self" ), "%1", maxMS );
	else if ( !bParty && !who.empty() )
		why = Arg( Arg( ui.Translate( "fui_reach_member" ), "%1", who ), "%2", maxMS );
	else
		why = Arg( ui.Translate( "fui_reach_party" ), "%1", maxMS );
	std::string best = server;
	if ( ms >= 0 )
		best += " \xC2\xB7 ~" + std::to_string( ms ) + " " + ui.Translate( "fui_ms" );
	FuiTF().Toast( "warn", ui.Translate( "fui_reach_title" ),
		why + " " + Arg( ui.Translate( "fui_reach_best" ), "%1", best ), "" );

	Node reach;
	reach[ "relaxed" ] = true;
	reach[ "why" ] = why;
	reach[ "best" ] = server;
	reach[ "best_ms" ] = ms;
	ui.Set( "reach", std::move( reach ) );
}

} // namespace

void FuiServersUpdate( fui::Runtime &ui )
{
	State_t &s = S();
	if ( !s.reach.empty() )
	{
		std::string json;
		json.swap( s.reach );
		ShowReach( ui, json );
	}
	static ConVarRef tf_mm_region( "tf_mm_region" );
	std::string pick = tf_mm_region.IsValid() ? tf_mm_region.GetString() : "auto";
	if ( pick.empty() )
		pick = "auto";

	Node region;
	region[ "pick" ] = pick;
	region[ "max_ms" ] = s.maxMS;
	Node &list = region[ "list" ];
	list.SetArray();
	if ( !s.regions.empty() )
	{
		Node &autoItem = list.Append();
		autoItem[ "id" ] = "auto";
		autoItem[ "name" ] = "";
		autoItem[ "ms" ] = -1;
		autoItem[ "ok" ] = true;
		autoItem[ "measured" ] = false;
		autoItem[ "on" ] = pick == "auto";
	}
	for ( const auto &r : s.regions )
	{
		int best = -1;
		bool bOK = false, bMeasured = false;
		for ( const Target_t &t : s.targets )
		{
			if ( t.region != r.first || !t.bDone )
				continue;
			bMeasured = true;
			if ( t.bOK )
			{
				bOK = bOK || s.maxMS < 0 || t.ms <= s.maxMS;
				// Keep the measurement even above the matchmaking preference.
				// A slow answer is still an answer, including through a relay.
				if ( t.ms >= 0 && ( best < 0 || t.ms < best ) )
					best = t.ms;
			}
		}
		Node &item = list.Append();
		item[ "id" ] = r.first;
		item[ "name" ] = r.second;
		item[ "ms" ] = best;
		item[ "ok" ] = bOK;
		item[ "measured" ] = bMeasured;
		item[ "on" ] = pick == r.first;
	}
	// Every server measured and none answers in time: the play screen warns
	// before the player even queues.
	bool bAnyMeasured = false, bAnyOK = false;
	for ( const Target_t &t : s.targets )
	{
		if ( !t.bDone )
			continue;
		bAnyMeasured = true;
		bAnyOK = bAnyOK || ( t.bOK && ( s.maxMS < 0 || t.ms <= s.maxMS ) );
	}
	region[ "none_ok" ] = bAnyMeasured && !bAnyOK;
	const std::string dump = region.Dump();
	if ( dump == s.shown )
		return;
	s.shown = dump;
	ui.Set( "region", std::move( region ) );
}

bool FuiServersCommand( fui::Runtime &ui, const std::string &name, const std::vector< std::string > &args )
{
	// A new search is judged afresh: the coordinator measures again.
	if ( name == "queue" )
	{
		Node reach;
		reach[ "relaxed" ] = false;
		ui.Set( "reach", std::move( reach ) );
		return false;
	}
	if ( name != "set_region" )
		return false;
	const std::string arg = args.empty() ? std::string() : args[ 0 ];
	bool bKnown = arg == "auto";
	for ( const auto &r : S().regions )
		bKnown = bKnown || r.first == arg;
	static ConVarRef tf_mm_region( "tf_mm_region" );
	if ( bKnown && tf_mm_region.IsValid() )
		tf_mm_region.SetValue( arg.c_str() );
	FuiServersUpdate( ui );
	return true;
}
