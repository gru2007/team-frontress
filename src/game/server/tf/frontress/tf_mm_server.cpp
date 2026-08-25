//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The game server's local game coordinator. See tf_mm_server.h.
//
//=============================================================================//

#include "cbase.h"

#include "tf_mm_server.h"

#include "fmtstr.h"
#include "gc_clientsystem.h"
#include "gcsdk/gcclient_sharedobjectcache.h"
#include "rtime.h"
#include "tf_gamerules.h"
#include "tf_gc_server.h"
#include "tf_lobby_server.h"
#include "tf_matchmaking_shared.h"
#include "tf_shareddefs.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

ConVar tf_mm_server_enable( "tf_mm_server_enable", "1", FCVAR_NONE,
                            "Act as the game coordinator for matches this server is given. "
                            "0 puts the server back to no-GC behaviour." );
ConVar tf_mm_server_debug( "tf_mm_server_debug", "0", FCVAR_NONE,
                           "Spew what the server-side matchmaking backend is doing." );

#define MMSrvDbg( ... ) do { if ( tf_mm_server_debug.GetBool() ) Msg( "[mmsrv] " __VA_ARGS__ ); } while ( false )

// The prefix every line meant for the log agent carries. It is grepped for by
// greyline-agent, so it is a protocol: do not change it without changing that.
#define TFMM_REPORT_PREFIX "[frontress]"

static CTFMMServer s_TFMMServer;
CTFMMServer *TFMMServer() { return &s_TFMMServer; }

//-----------------------------------------------------------------------------
// Protobuf wire encoding, by hand.
//
// Same reason as the client's copy in tf_mm_backend.cpp: the only entry point
// that subscribes a local shared object cache -- CGCClient::AddLocalSOCache --
// takes a serialized CMsgSOCacheSubscribed, whose generated header is inside
// the prebuilt gcsdk library and not part of this build. Generating it here
// would register a second copy of descriptors the library already owns and
// abort at startup, so the four fields are written by hand instead.
//
//   message CMsgSOCacheSubscribed {
//     message SubscribedType { optional int32 type_id = 1; repeated bytes object_data = 2; }
//     optional fixed64 owner = 1;
//     repeated SubscribedType objects = 2;
//   }
//-----------------------------------------------------------------------------
static void PBWriteVarint( CUtlBuffer &buf, uint64 value )
{
	do
	{
		uint8 byte = (uint8)( value & 0x7F );
		value >>= 7;
		if ( value != 0 )
			byte |= 0x80;
		buf.PutUnsignedChar( byte );
	} while ( value != 0 );
}

static void PBWriteTag( CUtlBuffer &buf, int nField, int nWireType )
{
	PBWriteVarint( buf, ( (uint64)nField << 3 ) | (uint64)nWireType );
}

static void PBWriteFixed64( CUtlBuffer &buf, int nField, uint64 value )
{
	PBWriteTag( buf, nField, 1 ); // 1 = 64-bit
	for ( int i = 0; i < 8; i++ )
		buf.PutUnsignedChar( (uint8)( ( value >> ( i * 8 ) ) & 0xFF ) );
}

static void PBWriteBytes( CUtlBuffer &buf, int nField, const void *pData, int nBytes )
{
	PBWriteTag( buf, nField, 2 ); // 2 = length-delimited
	PBWriteVarint( buf, (uint64)nBytes );
	if ( nBytes > 0 )
		buf.Put( pData, nBytes );
}

//-----------------------------------------------------------------------------
CTFMMServer::CTFMMServer()
	: CAutoGameSystemPerFrame( "CTFMMServer" )
	, m_bPublished( false )
	, m_bWarnedPublishFailed( false )
	, m_ulPlainMatchID( 0 )
	, m_bAwaitingMap( false )
{
}

//-----------------------------------------------------------------------------
bool CTFMMServer::Init()
{
	return true;
}

//-----------------------------------------------------------------------------
void CTFMMServer::Shutdown()
{
	DestroyLobby();
}

//-----------------------------------------------------------------------------
CSteamID CTFMMServer::OurSteamID() const
{
	// The cache the server-side matchmaking code listens to is keyed by the
	// game server's own Steam ID, which only exists once the GSLT has logged
	// in. No GSLT, no matchmaking: say so where an operator will see it
	// rather than failing quietly.
	const CSteamID *pID = engine ? engine->GetGameServerSteamID() : NULL;
	return pID ? *pID : CSteamID();
}

//-----------------------------------------------------------------------------
bool CTFMMServer::BActive() const
{
	return tf_mm_server_enable.GetBool() && OurSteamID().IsValid();
}

//-----------------------------------------------------------------------------
void CTFMMServer::FrameUpdatePreEntityThink()
{
	if ( !m_bPublished || !m_bAwaitingMap )
		return;

	// The lobby went in as SERVERSETUP, which is what makes CTFGCServerSystem
	// treat it as a new match and change the map for us. Once we are on that
	// map the lobby is RUN: that is the state the whole rest of the server --
	// and the clients reading their own copy -- expects during play.
	if ( !TFGameRules() || m_strMap.IsEmpty() )
		return;

	// STRING() answers "" for an unset map name, which never matches ours.
	const char *pszMap = STRING( gpGlobals->mapname );
	if ( !pszMap || V_stricmp( pszMap, m_strMap.Get() ) != 0 )
		return;

	m_msgLobby.set_state( CSOTFGameServerLobby_State_RUN );
	if ( !BPublishLobby() )
	{
		Warning( "[mmsrv] could not publish the RUN lobby state; will retry\n" );
		return;
	}
	m_bAwaitingMap = false;

	MMSrvDbg( "match %016llx is live on %s\n",
	          (unsigned long long)m_msgLobby.match_id(), m_strMap.Get() );
}

//-----------------------------------------------------------------------------
void CTFMMServer::BeginMatch( uint64 ulMatchID, int nMatchGroup, const char *pszMap,
                              const char *pszServerConfig, const char *pszFallbackPassword,
                              const CUtlVector< TFMMSeat_t > &vecSeats, int nMaxPlayers )
{
	if ( !BActive() )
	{
		m_ulPlainMatchID = ulMatchID;
		Warning( "[mmsrv] cannot start a match: no game server Steam ID yet. "
		         "Set sv_setsteamaccount to a Game Server Login Token and restart.\n" );
		// The coordinator hands the map change to this command and does not
		// do it itself -- it cannot tell a command that failed from one that
		// worked. Returning without changing the map leaves the match running
		// on whatever was already loaded, with everybody connecting to the
		// wrong game and no way to find out. Run it as an ordinary passworded
		// match instead: that is a worse match, not a broken one.
		FallBackToPlainMatch( pszMap );
		return;
	}

	if ( m_bPublished )
	{
		// The coordinator does not reuse a server without taking it back
		// first, so this is a bug somewhere -- but leaving the old lobby in
		// place would leave the server running a match nobody is in.
		Warning( "[mmsrv] replacing match %016llx with %016llx\n",
		         (unsigned long long)m_msgLobby.match_id(), (unsigned long long)ulMatchID );
		EndMatch( "replaced" );
	}

	m_strFallbackPassword = pszFallbackPassword ? pszFallbackPassword : "";
	m_strServerConfig = pszServerConfig ? pszServerConfig : "";
	m_ulPlainMatchID = 0;

	m_msgLobby.Clear();
	// The lobby id has to be non-zero and stable; the match id is the only
	// identifier both halves of this system already agree on.
	m_msgLobby.set_lobby_id( ulMatchID );
	m_msgLobby.set_match_id( ulMatchID );
	m_msgLobby.set_match_group( (uint32)nMatchGroup );
	m_msgLobby.set_map_name( pszMap ? pszMap : "" );
	m_msgLobby.set_server_id( OurSteamID().ConvertToUint64() );
	m_msgLobby.set_formed_time( CRTime::RTime32TimeCur() );
	// CMatchInfo snapshots this value when the lobby is created. It is
	// the capacity of the match, not merely the initial roster size.
	m_msgLobby.set_fixed_match_size( MAX( nMaxPlayers, vecSeats.Count() ) );
	m_msgLobby.set_late_join_eligible( true );

	// SERVERSETUP is the state CTFGCServerSystem reacts to by building a
	// CMatchInfo, exec'ing the match group's config and changing the map.
	m_msgLobby.set_state( CSOTFGameServerLobby_State_SERVERSETUP );

	FOR_EACH_VEC( vecSeats, i )
	{
		const TFMMSeat_t &seat = vecSeats[i];
		CTFLobbyPlayerProto *pMember = m_msgLobby.add_members();
		pMember->set_id( seat.ulSteamID );
		pMember->set_type( CTFLobbyPlayerProto_Type_MATCH_PLAYER );
		pMember->set_team( (TF_GC_TEAM)seat.nGCTeam );
		pMember->set_name( seat.strName.Get() );
		// RESERVATION_PENDING is how a seat becomes a CMatchInfo player:
		// UpdateConnectedPlayersAndServerInfo walks the lobby looking for
		// exactly this and adds anybody it finds.
		pMember->set_connect_state( CTFLobbyPlayerProto_ConnectState_RESERVATION_PENDING );
	}

	m_strMap = pszMap ? pszMap : "";
	m_bAwaitingMap = true;

	// Order matters, and it is not the obvious one.
	//
	// CTFGCServerSystem::SOCreated only builds a CMatchInfo when m_bMMServerMode
	// is already true -- otherwise it decides it was handed a lobby it did not
	// want and rejects it. SOCreated fires *inside* the publish below, so
	// server mode has to be on first. And server mode refuses to stay on while
	// sv_password is set (see OnMMServerModeChanged), so the password has to
	// come off first as well.
	//
	// That is the right trade rather than a workaround: the roster is a much
	// better door than a password. tf_mm_strict 1 makes
	// SteamIDAllowedToConnect the gate, and it lets in exactly the people the
	// coordinator put in this lobby -- a password that leaks lets in anybody
	// who has seen a screenshot.
	//
	// If the publish then fails, the fallback password goes straight back on,
	// because an unlocked server with no gate is the one outcome worse than a
	// match that never starts.
	static ConVarRef sv_password( "sv_password" );
	static ConVarRef tf_mm_servermode( "tf_mm_servermode" );
	static ConVarRef tf_mm_strict( "tf_mm_strict" );
	static ConVarRef tf_mm_trusted( "tf_mm_trusted" );

	sv_password.SetValue( "" );
	tf_mm_servermode.SetValue( 1 );
	tf_mm_strict.SetValue( 1 );
	tf_mm_trusted.SetValue( 1 );

	if ( BPublishLobby() )
	{
		Msg( "[mmsrv] match %016llx: %s, %d players, group %d, roster gate up\n",
		     (unsigned long long)ulMatchID, m_strMap.Get(), vecSeats.Count(), nMatchGroup );

		// Publishing ran CTFGCServerSystem::SOCreated, which ran the match
		// group's InitServerSettingsForMatch -- and that sets servercfgfile to
		// Valve's own config for the group and then changes the map. Our
		// ruleset has to win, and the only moment it can is right here: after
		// their value went in, before the map finishes loading and execs it.
		if ( m_strServerConfig.IsEmpty() )
			return;

		static ConVarRef servercfgfile( "servercfgfile" );
		static ConVarRef lservercfgfile( "lservercfgfile" );
		servercfgfile.SetValue( m_strServerConfig.Get() );
		lservercfgfile.SetValue( m_strServerConfig.Get() );
		MMSrvDbg( "server config for this match is %s\n", m_strServerConfig.Get() );
		return;
	}

	{
		m_ulPlainMatchID = ulMatchID;
		m_bAwaitingMap = false;
		tf_mm_servermode.SetValue( 0 );
		tf_mm_strict.SetValue( 0 );
		tf_mm_trusted.SetValue( 0 );
		if ( !m_strFallbackPassword.IsEmpty() )
		{
			Warning( "[mmsrv] falling back to the match password: the roster gate is not up\n" );
			sv_password.SetValue( m_strFallbackPassword.Get() );
		}
		else
		{
			Warning( "[mmsrv] !! this server has neither a roster gate nor a password. "
			         "Anybody can join. Give the coordinator a password to fall back on.\n" );
		}
		// The lobby is what would have changed the map. It did not, so we do.
		FallBackToPlainMatch( pszMap );
		return;
	}
}

//-----------------------------------------------------------------------------
// Purpose: Start the match the ordinary way, because the lobby could not.
//
//			Only the map change is left to do: everything else the coordinator
//			asked for -- the password, the tags, the ruleset -- was already
//			exec'd over RCON before this command ran.
//-----------------------------------------------------------------------------
void CTFMMServer::FallBackToPlainMatch( const char *pszMap )
{
	if ( !pszMap || !pszMap[0] )
		return;

	Warning( "[mmsrv] changing to %s without a lobby; this match runs as a plain server\n", pszMap );
	engine->ChangeLevel( pszMap, NULL );
}

//-----------------------------------------------------------------------------
// Purpose: Add seats to the match that is already running.
//
//			The roster gate is the lobby: SteamIDAllowedToConnect answers from
//			CMatchInfo, and CMatchInfo is built from the lobby's members. So
//			"let this player in" and "put them in the lobby" are the same
//			action, and there is no way to do the first without the second --
//			which is the whole reason this exists rather than some separate
//			allow-list that could drift from the match.
//
//			CTFGCServerSystem::SOUpdated is already looking for exactly this: a
//			lobby member in RESERVATION_PENDING that the match does not have
//			yet makes it acknowledge them on the spot.
//-----------------------------------------------------------------------------
int CTFMMServer::AddSeats( uint64 ulMatchID, const CUtlVector< TFMMSeat_t > &vecSeats )
{
	if ( !m_bPublished )
	{
		if ( m_ulPlainMatchID == ulMatchID )
			return -2; // deliberate password fallback; no roster gate exists
		Warning( "[mmsrv] cannot add players: this server has no match\n" );
		return -1;
	}

	// A late command for the previous match must not seat somebody in this
	// one. Match ids are the only thing both halves agree on, so they are the
	// check.
	if ( m_msgLobby.match_id() != ulMatchID )
	{
		Warning( "[mmsrv] refusing players for match %016llx: this server is running %016llx\n",
		         (unsigned long long)ulMatchID, (unsigned long long)m_msgLobby.match_id() );
		return -1;
	}

	// Keep an exact copy so a failed cache update cannot leave ghost seats in
	// the in-memory lobby while the coordinator puts those players back in queue.
	CSOTFGameServerLobby msgBefore;
	msgBefore.CopyFrom( m_msgLobby );

	int nAdded = 0;
	FOR_EACH_VEC( vecSeats, i )
	{
		const TFMMSeat_t &seat = vecSeats[i];

		// Somebody already in the match who is being moved, rather than added.
		// Re-seating them is right -- a backfill that lands on a player the
		// coordinator moved between teams should move them, not duplicate them.
		bool bFound = false;
		for ( int j = 0; j < m_msgLobby.members_size(); j++ )
		{
			if ( m_msgLobby.members( j ).id() != seat.ulSteamID )
				continue;

			CTFLobbyPlayerProto *pMember = m_msgLobby.mutable_members( j );
			pMember->set_team( (TF_GC_TEAM)seat.nGCTeam );
			// Only re-reserve somebody who is not here: a connected player
			// pushed back to RESERVATION_PENDING would be acknowledged again
			// and counted as a fresh arrival.
			if ( pMember->connect_state() == CTFLobbyPlayerProto_ConnectState_DISCONNECTED )
				pMember->set_connect_state( CTFLobbyPlayerProto_ConnectState_RESERVATION_PENDING );
			bFound = true;
			break;
		}
		if ( bFound )
			continue;

		CTFLobbyPlayerProto *pMember = m_msgLobby.add_members();
		pMember->set_id( seat.ulSteamID );
		pMember->set_type( CTFLobbyPlayerProto_Type_MATCH_PLAYER );
		pMember->set_team( (TF_GC_TEAM)seat.nGCTeam );
		pMember->set_name( seat.strName.Get() );
		pMember->set_connect_state( CTFLobbyPlayerProto_ConnectState_RESERVATION_PENDING );
		nAdded++;
	}

	// Bumping the version is what tells the server-side heartbeat that this
	// lobby is not the one it last acted on.
	m_msgLobby.set_lobby_mm_version( m_msgLobby.lobby_mm_version() + 1 );

	if ( !BPublishLobby() )
	{
		Warning( "[mmsrv] could not publish the lobby after adding %d players; "
		         "they will be turned away at the door\n", nAdded );

		// The coordinator treats this admission as failed and re-queues the
		// tickets. Roll our local roster back to the same truth. BPublishLobby
		// is retried with the old object as well in case its replace path had
		// already removed the previous cache object before the create failed.
		m_msgLobby.CopyFrom( msgBefore );
		if ( !BPublishLobby() )
			Warning( "[mmsrv] could not restore the lobby after a failed seat update\n" );
		return -1;
	}

	Msg( "[mmsrv] match %016llx: %d seat(s) added, %d in the match\n",
	     (unsigned long long)ulMatchID, nAdded, m_msgLobby.members_size() );
	return nAdded;
}

//-----------------------------------------------------------------------------
void CTFMMServer::EndMatch( const char *pszWhy )
{
	if ( !m_bPublished && m_ulPlainMatchID == 0 )
		return;

	const uint64 ulEndingMatchID = m_bPublished ? m_msgLobby.match_id() : m_ulPlainMatchID;
	Msg( "[mmsrv] match %016llx over (%s)\n",
	     (unsigned long long)ulEndingMatchID, pszWhy ? pszWhy : "no reason given" );

	if ( m_bPublished )
		DestroyLobby();
	m_ulPlainMatchID = 0;
	m_bAwaitingMap = false;
	m_strMap.Clear();

	// Put the server back the way we found it. A server handed back to the
	// pool still advertising itself as an official match, still gating on a
	// roster nobody holds, is a server the next match cannot use.
	static ConVarRef tf_mm_servermode( "tf_mm_servermode" );
	static ConVarRef tf_mm_strict( "tf_mm_strict" );
	static ConVarRef tf_mm_trusted( "tf_mm_trusted" );
	tf_mm_servermode.SetValue( 0 );
	tf_mm_strict.SetValue( 0 );
	tf_mm_trusted.SetValue( 0 );
	m_strFallbackPassword.Clear();
}

//-----------------------------------------------------------------------------
bool CTFMMServer::BEnsureCacheSubscribed()
{
	const CSteamID steamID = OurSteamID();
	if ( !steamID.IsValid() || !GCClientSystem() || !GCClientSystem()->GetGCClient() )
		return false;

	GCSDK::CGCClientSharedObjectCache *pCache = GCClientSystem()->GetSOCache( steamID );
	if ( pCache && pCache->BIsSubscribed() )
		return true;

	std::string strLobby;
	if ( !m_msgLobby.SerializeToString( &strLobby ) )
		return false;

	CUtlBuffer bufType( 0, 2048, 0 );
	PBWriteTag( bufType, 1, 0 ); // type_id, varint
	PBWriteVarint( bufType, (uint64)CTFGSLobby::k_nTypeID );
	PBWriteBytes( bufType, 2, strLobby.data(), (int)strLobby.size() );

	CUtlBuffer bufMsg( 0, 2176, 0 );
	PBWriteFixed64( bufMsg, 1, steamID.ConvertToUint64() );
	PBWriteBytes( bufMsg, 2, bufType.Base(), bufType.TellPut() );

	pCache = GCClientSystem()->GetGCClient()->AddLocalSOCache( steamID, bufMsg.Base(), (uint32)bufMsg.TellPut() );
	if ( !pCache || !pCache->BIsSubscribed() )
	{
		Warning( "[mmsrv] could not subscribe the game server's shared object cache; "
		         "this match will run as an ordinary community server\n" );
		return false;
	}

	// The lobby went in with the subscription, and the listener CTFGCServerSystem
	// registered on this cache has already been told about it.
	m_bPublished = true;
	m_strLastPublished = strLobby.c_str();

	MMSrvDbg( "subscribed the game server SO cache ourselves\n" );
	return true;
}

//-----------------------------------------------------------------------------
bool CTFMMServer::BPublishLobby()
{
	if ( !BEnsureCacheSubscribed() )
		return false;

	GCSDK::CGCClientSharedObjectCache *pCache = GCClientSystem()->GetSOCache( OurSteamID() );
	if ( !pCache )
		return false;

	std::string strData;
	if ( !m_msgLobby.SerializeToString( &strData ) )
		return false;

	GCSDK::CSharedObjectTypeCache *pType = pCache->FindBaseTypeCache( CTFGSLobby::k_nTypeID );
	const bool bExists = ( pType && pType->GetCount() > 0 );

	// Republishing an unchanged object walks every listener for nothing, and
	// on this side a listener means "re-examine the whole match". The cache
	// object must still exist: a failed replace may have destroyed it before
	// the replacement create failed.
	if ( m_bPublished && bExists && strData == m_strLastPublished.Get() )
		return true;

	bool bOK = bExists
		? pCache->BUpdateFromMsg( CTFGSLobby::k_nTypeID, strData.data(), (uint32)strData.size() )
		: pCache->BCreateFromMsg( CTFGSLobby::k_nTypeID, strData.data(), (uint32)strData.size() );

	if ( !bOK && bExists && !m_strLastPublished.IsEmpty() )
	{
		// An update is aimed at the object we published last. Replace it
		// rather than talk past it -- same failure the client half hits when
		// the object's key field changes underneath an update.
		pCache->BDestroyFromMsg( CTFGSLobby::k_nTypeID,
		                         m_strLastPublished.Get(), (uint32)m_strLastPublished.Length() );
		bOK = pCache->BCreateFromMsg( CTFGSLobby::k_nTypeID, strData.data(), (uint32)strData.size() );
	}

	if ( bOK )
	{
		m_bPublished = true;
		m_strLastPublished = strData.c_str();
		m_bWarnedPublishFailed = false;
		return true;
	}

	if ( !m_bWarnedPublishFailed )
	{
		m_bWarnedPublishFailed = true;
		Warning( "[mmsrv] could not publish the lobby object (%s, %d bytes); "
		         "this match will run as an ordinary community server\n",
		         bExists ? "update" : "create", (int)strData.size() );
	}
	return false;
}

//-----------------------------------------------------------------------------
void CTFMMServer::DestroyLobby()
{
	if ( !m_bPublished )
		return;

	GCSDK::CGCClientSharedObjectCache *pCache =
		GCClientSystem() ? GCClientSystem()->GetSOCache( OurSteamID() ) : NULL;
	if ( pCache && !m_strLastPublished.IsEmpty() )
	{
		pCache->BDestroyFromMsg( CTFGSLobby::k_nTypeID,
		                         m_strLastPublished.Get(), (uint32)m_strLastPublished.Length() );
	}

	m_bPublished = false;
	m_strLastPublished.Clear();
	m_msgLobby.Clear();
}

//-----------------------------------------------------------------------------
void CTFMMServer::ReportLine( const char *pszEvent, const char *pszBody ) const
{
	// This has to go through the *log*, not the console. logaddress_add
	// forwards what engine->LogPrint writes -- with the "L <date> - <time>: "
	// stamp greyline-agent looks for to find a line at all -- and console
	// spew from Msg never reaches it. UTIL_LogPrintf is that path, and it
	// echoes to the console anyway. No new socket in the game DLL.
	UTIL_LogPrintf( TFMM_REPORT_PREFIX " %s %016llx %s\n",
	                pszEvent, (unsigned long long)m_msgLobby.match_id(), pszBody ? pszBody : "" );
}

//-----------------------------------------------------------------------------
// Downcast a protobuf message we already identified by its EMsg. The descriptor
// check is what makes it safe.
template < typename T >
static const T *ProtoAs( const ::google::protobuf::Message &msg )
{
	if ( msg.GetDescriptor() != T::descriptor() )
	{
		Warning( "[mmsrv] message arrived as %s, expected %s\n",
		         msg.GetDescriptor() ? msg.GetDescriptor()->full_name().c_str() : "?",
		         T::descriptor()->full_name().c_str() );
		return NULL;
	}
	return static_cast< const T * >( &msg );
}

//-----------------------------------------------------------------------------
bool CTFMMServer::BHandleServerMsg( uint32 unMsgType, const ::google::protobuf::Message &msgRequest,
                                    ::google::protobuf::Message *pMsgReply )
{
	if ( !BActive() )
		return false;

	switch ( unMsgType )
	{
		case k_EMsgGC_Match_Result:
		{
			// The whole point of having a real CMatchInfo: the game itself
			// says who won, what the score was and who played, with per-player
			// detail nothing outside the game can reconstruct.
			const CMsgGC_Match_Result *pMsg = ProtoAs< CMsgGC_Match_Result >( msgRequest );
			if ( !pMsg )
				return false;

			CUtlString strBody;
			strBody.Format( "status=%d winner=%u red=%u blu=%u duration=%u bots=%u players=%d",
			                (int)pMsg->status(),
			                pMsg->winning_team(),
			                pMsg->red_score(),
			                pMsg->blue_score(),
			                pMsg->duration(),
			                pMsg->bots(),
			                pMsg->players_size() );
			ReportLine( "match_result", strBody.Get() );

			for ( int i = 0; i < pMsg->players_size(); i++ )
			{
				const CMsgGC_Match_Result_Player &player = pMsg->players( i );
				CUtlString strPlayer;
				strPlayer.Format( "steamid=%llu team=%u score=%u kills=%u deaths=%u damage=%u healing=%u support=%u leave=%d",
				                  (unsigned long long)player.steam_id(),
				                  player.team(),
				                  player.score(),
				                  player.kills(),
				                  player.deaths(),
				                  player.damage(),
				                  player.healing(),
				                  player.support(),
				                  (int)player.leave_reason() );
				ReportLine( "match_player", strPlayer.Get() );
			}
			ReportLine( "match_result_end", "" );
			return true;
		}

		case k_EMsgGCPlayerLeftMatch:
		{
			// An abandon, as the server saw it. The coordinator keeps the
			// record; it only has to be told.
			const CMsgPlayerLeftMatch *pMsg = ProtoAs< CMsgPlayerLeftMatch >( msgRequest );
			if ( !pMsg )
				return false;

			CUtlString strBody;
			strBody.Format( "steamid=%llu reason=%d abandon=%d",
			                (unsigned long long)pMsg->steam_id(),
			                (int)pMsg->leave_reason(),
			                pMsg->was_abandon() ? 1 : 0 );
			ReportLine( "player_left", strBody.Get() );
			return true;
		}

		case k_EMsgGCGameServerKickingLobby:
		{
			// The server is done with this lobby. Nothing to ask anybody.
			ReportLine( "match_closed", "" );
			return true;
		}

		case k_EMsgGC_NewMatchForLobbyRequest:
		{
			// Rolling matches -- the GC handing the same lobby a new map when
			// one finishes -- are the coordinator's job, not the server's. Say
			// no rather than leave the server waiting: the honest answer keeps
			// the match ending cleanly instead of hanging on a rematch that is
			// never granted.
			CMsgGCNewMatchForLobbyResponse *pReply =
				pMsgReply ? static_cast< CMsgGCNewMatchForLobbyResponse * >( pMsgReply ) : NULL;
			if ( pReply )
				pReply->set_success( false );
			return true;
		}

		case k_EMsgGC_ChangeMatchPlayerTeamsRequest:
		{
			// The server rebalanced and is telling the GC. Ours to record: the
			// lobby is the server's own source of truth for who is on which
			// side, and letting it drift would undo the balance next time
			// anything re-read it.
			const CMsgGCChangeMatchPlayerTeamsRequest *pMsg =
				ProtoAs< CMsgGCChangeMatchPlayerTeamsRequest >( msgRequest );
			if ( pMsg )
			{
				for ( int i = 0; i < pMsg->member_size(); i++ )
				{
					const uint64 ulID = pMsg->member( i ).member_id();
					for ( int j = 0; j < m_msgLobby.members_size(); j++ )
					{
						if ( m_msgLobby.members( j ).id() == ulID )
						{
							m_msgLobby.mutable_members( j )->set_team( pMsg->member( i ).new_team() );
							break;
						}
					}
				}
				BPublishLobby();
			}

			CMsgGCChangeMatchPlayerTeamsResponse *pReply =
				pMsgReply ? static_cast< CMsgGCChangeMatchPlayerTeamsResponse * >( pMsgReply ) : NULL;
			if ( pReply )
				pReply->set_success( true );
			return true;
		}

		case k_EMsgGCVoteKickPlayerRequest:
		{
			// Whether somebody may start a vote kick is a policy question the
			// GC answered with a rate limit we have no equivalent of. Allow
			// it; the server still requires the vote to pass.
			CMsgGC_VoteKickPlayerRequestResponse *pReply =
				pMsgReply ? static_cast< CMsgGC_VoteKickPlayerRequestResponse * >( pMsgReply ) : NULL;
			if ( pReply )
			{
				pReply->set_allowed( true );
				pReply->set_voter_inhibit( 0 );
				pReply->set_target_inhibit( 0 );
			}
			return true;
		}

		case k_EMsgGC_ProcessMatchVoteKick:
		{
			// The vote already passed on the server. The GC's answer was
			// whether to actually remove them from the match, and the only
			// answer that makes a passed kick mean anything is yes.
			const CMsgProcessMatchVoteKick *pMsg = ProtoAs< CMsgProcessMatchVoteKick >( msgRequest );
			if ( pMsg )
			{
				CUtlString strBody;
				strBody.Format( "steamid=%llu by=%llu",
				                (unsigned long long)pMsg->target_steam_id(),
				                (unsigned long long)pMsg->initiator_steam_id() );
				ReportLine( "vote_kick", strBody.Get() );
			}

			CMsgProcessMatchVoteKickResponse *pReply =
				pMsgReply ? static_cast< CMsgProcessMatchVoteKickResponse * >( pMsgReply ) : NULL;
			if ( pReply )
				pReply->set_rip( true );
			return true;
		}

		case k_EMsgGCMvMVictory:
			// No Mann Up, no tour, nothing to award. Answered so the queue
			// does not stall if a MvM map is ever run through matchmaking.
			return true;

		// Fire-and-forget. These arrive through the BSendMessage hook, and
		// pMsgReply is not looked at.
		case k_EMsgGC_DailyCompetitiveStatsRollup:
			// Performance medals are ranked against a daily distribution the
			// GC computed. There is none, so nobody gets a medal -- which is
			// what happens anyway when the reply never arrives, except that
			// this way the reliable queue is not left holding it.
			return true;

		case k_EMsgGCGameServerMatchmakingStatus:
		case k_EMsgGC_GameServer_UpdateData:
			// Heartbeats towards a GC that is not listening. The coordinator
			// learns the same things over RCON and from the log agent.
			return true;

		default:
			break;
	}

	return false;
}

//-----------------------------------------------------------------------------
void CTFMMServer::Spew() const
{
	Msg( "Team Frontress server backend\n" );
	Msg( "  active:      %s\n", BActive() ? "yes" : "no" );
	Msg( "  server id:   %s\n", OurSteamID().IsValid() ? OurSteamID().Render() : "none (no GSLT?)" );
	Msg( "  lobby:       %s\n", m_bPublished ? "published" : "none" );
	if ( m_bPublished )
	{
		Msg( "  match:       %016llx\n", (unsigned long long)m_msgLobby.match_id() );
		Msg( "  match group: %d\n", m_msgLobby.match_group() );
		Msg( "  map:         %s\n", m_msgLobby.map_name().c_str() );
		Msg( "  state:       %s\n", CSOTFGameServerLobby_State_Name( m_msgLobby.state() ).c_str() );
		Msg( "  seats:       %d\n", m_msgLobby.members_size() );
	}

	CMatchInfo *pMatch = GTFGCClientSystem() ? GTFGCClientSystem()->GetMatch() : NULL;
	Msg( "  match info:  %s\n", pMatch ? "built" : "none -- the server does not think it is in a match" );
}

//
// The coordinator's side of the wire. These are what it sends over RCON.
//

//-----------------------------------------------------------------------------
// Match ids are hex everywhere else in this system -- they are what goes into
// sv_tags as "tfmm:<id>" and what the coordinator logs -- so they are parsed as
// hex here. V_atoui64 would read "5c8cb6f2b25ed652" as 5, and every match would
// collide on the same lobby id.
static uint64 ParseHex64( const char *pszHex )
{
	uint64 ulValue = 0;
	for ( const char *p = pszHex; p && *p; p++ )
	{
		uint64 nDigit;
		if ( *p >= '0' && *p <= '9' )
			nDigit = (uint64)( *p - '0' );
		else if ( *p >= 'a' && *p <= 'f' )
			nDigit = (uint64)( *p - 'a' ) + 10;
		else if ( *p >= 'A' && *p <= 'F' )
			nDigit = (uint64)( *p - 'A' ) + 10;
		else
			return 0; // not a match id at all

		ulValue = ( ulValue << 4 ) | nDigit;
	}
	return ulValue;
}

//-----------------------------------------------------------------------------
// Parse "steamid:team[:name],steamid:team,..." -- the one argument that carries
// a roster. team is the *game* team number the coordinator assigned (2 RED,
// 3 BLU), which is the same thing it tells the clients, so the two cannot
// disagree about which side anybody is on.
static void ParseSeats( const char *pszRoster, CUtlVector< TFMMSeat_t > &vecOut )
{
	CUtlVector< CUtlString > vecEntries;
	V_SplitString( pszRoster, ",", vecEntries );
	FOR_EACH_VEC( vecEntries, i )
	{
		CUtlVector< CUtlString > vecParts;
		V_SplitString( vecEntries[i].Get(), ":", vecParts );
		if ( vecParts.Count() < 2 )
			continue;

		const uint64 ulID = V_atoui64( vecParts[0].Get() );
		if ( ulID == 0 )
			continue;

		const int nGameTeam = atoi( vecParts[1].Get() );

		TFMMSeat_t seat;
		seat.ulSteamID = ulID;
		// The lobby speaks in the GC's teams, where the defenders are red.
		// Same mapping the client uses when it reads an assignment.
		if ( nGameTeam == TF_TEAM_RED )
			seat.nGCTeam = TF_GC_TEAM_DEFENDERS;
		else if ( nGameTeam == TF_TEAM_BLUE )
			seat.nGCTeam = TF_GC_TEAM_INVADERS;
		else
			seat.nGCTeam = TF_GC_TEAM_PLAYER_POOL;

		if ( vecParts.Count() >= 3 )
			seat.strName = vecParts[2];

		vecOut.AddToTail( seat );
	}
}

//-----------------------------------------------------------------------------
// tf_mm_match_begin <match_id hex> <match_group> <map> <server_cfg> <fallback_password> <steamid:team[:name],...>
//
// team is the *game* team number the coordinator assigned -- 2 RED, 3 BLU --
// which is the same thing it tells the clients, so the two cannot disagree.
//-----------------------------------------------------------------------------
CON_COMMAND( tf_mm_match_begin, "Take a match from the matchmaking coordinator." )
{
	if ( args.ArgC() < 7 )
	{
		Msg( "Usage: tf_mm_match_begin <match_id> <match_group> <map> <server_cfg> "
		     "<fallback_password> <steamid:team,...>\n" );
		return;
	}

	const uint64 ulMatchID = ParseHex64( args[1] );
	if ( ulMatchID == 0 )
	{
		Warning( "tf_mm_match_begin: '%s' is not a match id\n", args[1] );
		return;
	}

	const int nMatchGroup = atoi( args[2] );
	const char *pszMap = args[3];

	const char *pszServerConfig = args[4];
	const char *pszFallbackPassword = args[5];

	CUtlVector< TFMMSeat_t > vecSeats;
	ParseSeats( args[6], vecSeats );
	const int nMaxPlayers = args.ArgC() >= 8
		? MAX( atoi( args[7] ), vecSeats.Count() )
		: vecSeats.Count();

	if ( vecSeats.Count() == 0 )
	{
		// Still change the map: the coordinator has already told the clients
		// where to go and is not going to send a changelevel of its own.
		Warning( "tf_mm_match_begin: no valid seats in '%s'; running %s as a plain server\n",
		         args[6], pszMap );
		if ( pszMap && pszMap[0] )
			engine->ChangeLevel( pszMap, NULL );
		return;
	}

	TFMMServer()->BeginMatch( ulMatchID, nMatchGroup, pszMap, pszServerConfig,
	                          pszFallbackPassword, vecSeats, nMaxPlayers );
}

//-----------------------------------------------------------------------------
// tf_mm_match_add <match_id hex> <steamid:team[:name],...>
//
// Backfill and standby. Everything that lets somebody into a running match goes
// through here, because the lobby *is* the gate.
//-----------------------------------------------------------------------------
CON_COMMAND( tf_mm_match_add, "Add players to the match this server is running." )
{
	if ( args.ArgC() < 3 )
	{
		Msg( "Usage: tf_mm_match_add <match_id> <steamid:team,...>\n" );
		return;
	}

	const uint64 ulMatchID = ParseHex64( args[1] );
	if ( ulMatchID == 0 )
	{
		Warning( "tf_mm_match_add: '%s' is not a match id\n", args[1] );
		return;
	}

	CUtlVector< TFMMSeat_t > vecSeats;
	ParseSeats( args[2], vecSeats );
	if ( vecSeats.Count() == 0 )
	{
		Warning( "tf_mm_match_add: no valid seats in '%s'\n", args[2] );
		return;
	}

	const int nAdded = TFMMServer()->AddSeats( ulMatchID, vecSeats );
	if ( nAdded == -2 )
	{
		Msg( "TFMM_MATCH_ADD_PLAIN %s\n", args[1] );
		return;
	}
	if ( nAdded < 0 )
	{
		Msg( "TFMM_MATCH_ADD_FAILED %s\n", args[1] );
		return;
	}
	Msg( "TFMM_MATCH_ADD_OK %s %d\n", args[1], nAdded );
}

//-----------------------------------------------------------------------------
CON_COMMAND( tf_mm_match_end, "Give the current matchmaking match back." )
{
	TFMMServer()->EndMatch( args.ArgC() > 1 ? args[1] : "coordinator asked" );
}

//-----------------------------------------------------------------------------
CON_COMMAND( tf_mm_server_status, "Print what the server-side matchmaking backend is doing." )
{
	TFMMServer()->Spew();
}
