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
#include "tf_match_description.h"
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
	, m_bWarnedPasswordReturned( false )
	, m_bWarnedGateDropped( false )
	, m_bWarnedAdmissionsStuck( false )
	, m_bAdmissionsReported( false )
	, m_flNextAdmissionCheck( 0.0f )
	, m_nAdmissionNudges( 0 )
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
	// A match that deliberately runs as a plain passworded server has no gate
	// to hold up and no roster to check -- and clearing its password here
	// would be the one thing keeping strangers out.
	if ( !m_bPublished || m_ulPlainMatchID != 0 )
		return;

	EnforceRosterGate();

	if ( !m_bAwaitingMap )
	{
		VerifyAdmissions();
		return;
	}

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

	// The map load is exactly where the gate is most likely to have been
	// undone -- and where the roster has to be checked for real, because the
	// reservation pass that admits it may have been refused before the new
	// map's slot count existed. Check on the next frame rather than in 2s.
	m_flNextAdmissionCheck = 0.0f;

	MMSrvDbg( "match %016llx is live on %s\n",
	          (unsigned long long)m_msgLobby.match_id(), m_strMap.Get() );
}

//-----------------------------------------------------------------------------
// Purpose: Keep the door we replaced the password with from falling off quietly.
//
//			Two pieces of stock behaviour conspire. The map change the lobby
//			asks for execs whatever servercfgfile points at, and on a rented
//			server that file is the host's, not ours -- serveme's
//			reservation.cfg writes sv_password into it, and so does the stock
//			server.cfg. And CTFGCServerSystem::PreClientUpdate turns
//			tf_mm_servermode off the very next frame after it sees a password,
//			because a matchmaking server is not allowed to hold one.
//
//			Put together: the map finishes loading, a password comes back,
//			server mode goes off, and the roster gate is gone -- while the
//			lobby object is still published and every log line still looks
//			right. The players then arrive holding the password the
//			coordinator gave them, which is not the one the server just
//			exec'd, and not one of them gets in.
//
//			So both halves are re-asserted for as long as this match owns the
//			server. The password has to be cleared first: setting server mode
//			back while one is set only gets it turned off again next frame.
//-----------------------------------------------------------------------------
void CTFMMServer::EnforceRosterGate()
{
	static ConVarRef sv_password( "sv_password" );
	static ConVarRef tf_mm_servermode( "tf_mm_servermode" );
	static ConVarRef tf_mm_strict( "tf_mm_strict" );

	const char *pszPassword = sv_password.GetString();
	if ( pszPassword && pszPassword[0] )
	{
		if ( !m_bWarnedPasswordReturned )
		{
			m_bWarnedPasswordReturned = true;
			Warning( "[mmsrv] sv_password was set during match %016llx -- clearing it. "
			         "A matchmaking server cannot hold a password; the roster is the gate. "
			         "Check what the map load exec'd.\n",
			         (unsigned long long)m_msgLobby.match_id() );
		}
		sv_password.SetValue( "" );
	}

	if ( tf_mm_servermode.GetInt() != 1 || tf_mm_strict.GetInt() != 1 )
	{
		if ( !m_bWarnedGateDropped )
		{
			m_bWarnedGateDropped = true;
			Warning( "[mmsrv] the roster gate was turned off under match %016llx "
			         "(tf_mm_servermode %d, tf_mm_strict %d) -- putting it back\n",
			         (unsigned long long)m_msgLobby.match_id(),
			         tf_mm_servermode.GetInt(), tf_mm_strict.GetInt() );
		}
		// tf_mm_trusted is not part of the gate -- it is how the server
		// advertises itself -- so whatever the coordinator asked for is left
		// alone here.
		tf_mm_servermode.SetValue( 1 );
		tf_mm_strict.SetValue( 1 );
	}
}

//-----------------------------------------------------------------------------
// Purpose: Check the two things that have to be true for a seat to be a way in,
//			rather than assuming they still are because they once were.
//
//			The roster reaches the rest of the server as one shared object, and
//			the list of SteamIDs allowed to connect is derived from it -- so if
//			that object leaves the cache, every player is turned away while
//			nothing in this class notices. The client half already learned that
//			a cache can be replaced wholesale underneath the thing that
//			published into it; there is no reason to find out the same way here.
//
//			And publishing the roster is not the same as the roster being
//			admitted. CMatchInfo is filled by the stock reservation pass, which
//			can decline -- most plausibly because it ran before the map change
//			gave the server its real slot count. Nothing retries that on its
//			own once the lobby has stopped changing, so ask the same function
//			the engine asks on every connection, and if the answer is no, send
//			the signal the GC would have: a new lobby version, which
//			CTFGCServerSystem::SOUpdated answers with an acknowledgement pass.
//-----------------------------------------------------------------------------
void CTFMMServer::VerifyAdmissions()
{
	// Twice a second would be free; every couple of seconds is enough for
	// something that only has to converge before players finish loading.
	const float flNow = (float)Plat_FloatTime();
	if ( flNow < m_flNextAdmissionCheck )
		return;
	m_flNextAdmissionCheck = flNow + 2.0f;

	GCSDK::CGCClientSharedObjectCache *pCache =
		GCClientSystem() ? GCClientSystem()->GetSOCache( OurSteamID() ) : NULL;
	GCSDK::CSharedObjectTypeCache *pType =
		pCache ? pCache->FindBaseTypeCache( CTFGSLobby::k_nTypeID ) : NULL;
	if ( !pCache || !pCache->BIsSubscribed() || !pType || pType->GetCount() == 0 )
	{
		Warning( "[mmsrv] match %016llx is no longer in the game server's shared object "
		         "cache -- republishing the roster\n",
		         (unsigned long long)m_msgLobby.match_id() );
		// Publishing skips an object it believes is already there and
		// unchanged. Forget what we last wrote so the create path is taken.
		m_strLastPublished.Clear();
		if ( !BPublishLobby() )
			return;
	}

	CTFGCServerSystem *pGC = GTFGCClientSystem();
	if ( !pGC )
		return;

	int nAdmitted = 0;
	int nRefused = 0;
	for ( int i = 0; i < m_msgLobby.members_size(); i++ )
	{
		const CTFLobbyPlayerProto &member = m_msgLobby.members( i );
		// Only seats that still have to walk in. Somebody the match dropped --
		// an abandon, a vote kick -- is refused on purpose, and chasing that
		// would mean nudging the lobby for the rest of the match.
		if ( member.connect_state() != CTFLobbyPlayerProto_ConnectState_RESERVATION_PENDING &&
		     member.connect_state() != CTFLobbyPlayerProto_ConnectState_RESERVED )
		{
			continue;
		}

		const CSteamID steamID( member.id() );
		if ( steamID.IsValid() && pGC->SteamIDAllowedToConnect( steamID ) )
			nAdmitted++;
		else
			nRefused++;
	}

	if ( nRefused == 0 )
	{
		if ( !m_bAdmissionsReported )
		{
			m_bAdmissionsReported = true;
			m_nAdmissionNudges = 0;
			m_bWarnedAdmissionsStuck = false;
			Msg( "[mmsrv] match %016llx: the roster gate admits all %d waiting seat(s)\n",
			     (unsigned long long)m_msgLobby.match_id(), nAdmitted );
		}
		return;
	}

	m_bAdmissionsReported = false;

	// Bounded: a nudge that has not worked twenty times over is not going to,
	// and an operator should be told rather than have it retried all match.
	const int k_nMaxAdmissionNudges = 20;
	if ( m_nAdmissionNudges >= k_nMaxAdmissionNudges )
	{
		if ( !m_bWarnedAdmissionsStuck )
		{
			m_bWarnedAdmissionsStuck = true;
			Warning( "[mmsrv] !! match %016llx: %d seat(s) are still not admitted by the "
			         "roster gate after %d attempts. Those players cannot connect. "
			         "Check maxplayers against the match size and tf_mm_server_status.\n",
			         (unsigned long long)m_msgLobby.match_id(), nRefused, k_nMaxAdmissionNudges );
		}
		return;
	}

	m_nAdmissionNudges++;
	MMSrvDbg( "%d seat(s) not admitted yet; asking for an acknowledgement pass (%d)\n",
	          nRefused, m_nAdmissionNudges );
	m_msgLobby.set_lobby_mm_version( m_msgLobby.lobby_mm_version() + 1 );
	BPublishLobby();
}

//-----------------------------------------------------------------------------
bool CTFMMServer::BeginMatch( uint64 ulMatchID, int nMatchGroup, const char *pszMap,
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
		return false;
	}

	// CTFGCServerSystem::SOCreated goes straight from the published lobby into
	// GetMatchGroupDescription( group )->InitServerSettingsForMatch() with no
	// null check, and only some groups have a description compiled in -- 9v9,
	// 12v12 ladder and the smaller casual sizes do not. A group nothing
	// registered would crash the server the moment the lobby went in, which is
	// a much worse way to learn about a misconfigured coordinator than a match
	// that refuses to start.
	if ( !GetMatchGroupDescription( (ETFMatchGroup)nMatchGroup ) )
	{
		m_ulPlainMatchID = ulMatchID;
		Warning( "[mmsrv] match group %d is not one this build knows how to run. "
		         "Use a group with a match description (0 and 1 MvM, 2 ladder 6v6, "
		         "7 casual 12v12) -- refusing match %016llx.\n",
		         nMatchGroup, (unsigned long long)ulMatchID );
		m_strFallbackPassword = pszFallbackPassword ? pszFallbackPassword : "";
		if ( !m_strFallbackPassword.IsEmpty() )
		{
			static ConVarRef sv_password_refuse( "sv_password" );
			sv_password_refuse.SetValue( m_strFallbackPassword.Get() );
		}
		FallBackToPlainMatch( pszMap );
		return false;
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
	m_bWarnedPasswordReturned = false;
	m_bWarnedGateDropped = false;
	m_bWarnedAdmissionsStuck = false;
	m_bAdmissionsReported = false;
	m_flNextAdmissionCheck = 0.0f;
	m_nAdmissionNudges = 0;

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
			return true;

		static ConVarRef servercfgfile( "servercfgfile" );
		static ConVarRef lservercfgfile( "lservercfgfile" );
		servercfgfile.SetValue( m_strServerConfig.Get() );
		lservercfgfile.SetValue( m_strServerConfig.Get() );
		MMSrvDbg( "server config for this match is %s\n", m_strServerConfig.Get() );
		return true;
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
		return false;
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

	// The seats just sold have to be admitted too, and the same reservation
	// pass can decline them -- a full server is the ordinary reason.
	m_bAdmissionsReported = false;
	m_bWarnedAdmissionsStuck = false;
	m_nAdmissionNudges = 0;
	m_flNextAdmissionCheck = 0.0f;

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
	m_bWarnedPasswordReturned = false;
	m_bWarnedGateDropped = false;
	m_bWarnedAdmissionsStuck = false;
	m_bAdmissionsReported = false;
	m_nAdmissionNudges = 0;

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
// Purpose: Act on the game server's acknowledgement of lobby reservations.
//
//			The stock flow has two distinct halves. The GC offers a player by
//			putting them in RESERVATION_PENDING, then the game server allocates a
//			CMatchInfo slot and answers with RESERVED. Only after the GC writes
//			that acknowledgement back to the lobby may the player connect.
//
//			Simply swallowing this heartbeat leaves every member pending forever.
//			SteamIDAllowedToConnect deliberately rejects pending members, which
//			makes a coordinator-issued `connect ... matchmaking` look like an
//			ad-hoc join to the player. Mirror the missing GC transition here.
//-----------------------------------------------------------------------------
bool CTFMMServer::BApplyMatchmakingStatus( const CMsgGameServerMatchmakingStatus &msgStatus )
{
	// During the first BPublishLobby, SOCreated sends the acknowledgement
	// synchronously from inside AddLocalSOCache. m_bPublished is not latched
	// until that call returns, but m_msgLobby is already the live match.
	if ( !m_msgLobby.has_match_id() || m_msgLobby.match_id() == 0 )
		return true;

	CSOTFGameServerLobby msgBefore;
	msgBefore.CopyFrom( m_msgLobby );

	int nReserved = 0;
	int nConnected = 0;
	int nDisconnected = 0;
	bool bChanged = false;

	for ( int i = 0; i < msgStatus.players_size(); i++ )
	{
		const CMsgGameServerMatchmakingStatus_Player &statusPlayer = msgStatus.players( i );
		if ( statusPlayer.steam_id() == 0 )
			continue;

		for ( int j = 0; j < m_msgLobby.members_size(); j++ )
		{
			CTFLobbyPlayerProto *pMember = m_msgLobby.mutable_members( j );
			if ( pMember->id() != statusPlayer.steam_id() ||
			     pMember->type() != CTFLobbyPlayerProto_Type_MATCH_PLAYER )
			{
				continue;
			}

			CTFLobbyPlayerProto_ConnectState eNewState;
			switch ( statusPlayer.connect_state() )
			{
				case CMsgGameServerMatchmakingStatus_PlayerConnectState_RESERVED:
					// RESERVED acknowledges a pending first connection. If this
					// player was already connected, the same status means the
					// server still holds their reconnect reservation after they
					// left; preserve that distinction in the lobby.
					eNewState =
						( pMember->connect_state() == CTFLobbyPlayerProto_ConnectState_CONNECTED ||
						  pMember->connect_state() == CTFLobbyPlayerProto_ConnectState_DISCONNECTED )
						? CTFLobbyPlayerProto_ConnectState_DISCONNECTED
						: CTFLobbyPlayerProto_ConnectState_RESERVED;
					break;
				case CMsgGameServerMatchmakingStatus_PlayerConnectState_CONNECTED:
					eNewState = CTFLobbyPlayerProto_ConnectState_CONNECTED;
					break;
				default:
					continue;
			}

			if ( pMember->connect_state() != eNewState )
			{
				pMember->set_connect_state( eNewState );
				bChanged = true;
				if ( eNewState == CTFLobbyPlayerProto_ConnectState_RESERVED )
					nReserved++;
				else if ( eNewState == CTFLobbyPlayerProto_ConnectState_CONNECTED )
					nConnected++;
				else
					nDisconnected++;
			}
			break;
		}
	}

	if ( !bChanged )
		return true;

	// A state transition is a new lobby version just like a late-join roster
	// change. The next heartbeat then confirms it has observed this version.
	m_msgLobby.set_lobby_mm_version( m_msgLobby.lobby_mm_version() + 1 );
	if ( BPublishLobby() )
	{
		MMSrvDbg( "match %016llx acknowledged %d reservation(s), %d connection(s), %d disconnection(s)\n",
		          (unsigned long long)m_msgLobby.match_id(), nReserved, nConnected, nDisconnected );
		return true;
	}

	// Keep our private copy and the shared-object cache on the same truth. A
	// later heartbeat will retry the transition; until then the strict gate
	// remains safely closed rather than admitting a seat the lobby did not see.
	Warning( "[mmsrv] could not publish %d reservation, %d connection and %d disconnection "
	         "acknowledgement(s); players may need to retry joining\n",
	         nReserved, nConnected, nDisconnected );
	m_msgLobby.CopyFrom( msgBefore );
	if ( !BPublishLobby() )
		Warning( "[mmsrv] could not restore the lobby after a failed acknowledgement update\n" );
	return true;
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
		{
			const CMsgGameServerMatchmakingStatus *pMsg =
				ProtoAs< CMsgGameServerMatchmakingStatus >( msgRequest );
			return pMsg ? BApplyMatchmakingStatus( *pMsg ) : false;
		}

		case k_EMsgGC_GameServer_UpdateData:
			// General server-data heartbeat towards a GC that is not listening.
			// The coordinator learns the same things over RCON and from the log
			// agent. Matchmaking status is handled separately above because its
			// player acknowledgements mutate the lobby.
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

	static ConVarRef sv_password( "sv_password" );
	static ConVarRef tf_mm_servermode( "tf_mm_servermode" );
	static ConVarRef tf_mm_strict( "tf_mm_strict" );
	Msg( "  gate:        servermode %d, strict %d, password %s\n",
	     tf_mm_servermode.GetInt(), tf_mm_strict.GetInt(),
	     ( sv_password.GetString() && sv_password.GetString()[0] ) ? "SET (gate is down)" : "none" );

	CTFGCServerSystem *pGC = GTFGCClientSystem();
	CMatchInfo *pMatch = pGC ? pGC->GetMatch() : NULL;
	Msg( "  match info:  %s\n", pMatch ? "built" : "none -- the server does not think it is in a match" );

	// The whole point of the lobby is this answer, so print it per seat: it is
	// the same call the engine makes when a player knocks on the door.
	if ( m_bPublished && pGC )
	{
		for ( int i = 0; i < m_msgLobby.members_size(); i++ )
		{
			const CTFLobbyPlayerProto &member = m_msgLobby.members( i );
			const CSteamID steamID( member.id() );
			Msg( "    %s  team %d  %-20s  %s\n",
			     steamID.Render(),
			     (int)member.team(),
			     CTFLobbyPlayerProto_ConnectState_Name( member.connect_state() ).c_str(),
			     pGC->SteamIDAllowedToConnect( steamID ) ? "may connect" : "REFUSED" );
		}
	}
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

	if ( TFMMServer()->BeginMatch( ulMatchID, nMatchGroup, pszMap, pszServerConfig,
	                              pszFallbackPassword, vecSeats, nMaxPlayers ) )
		Msg( "TFMM_MATCH_BEGIN_OK %s\n", args[1] );
	else
		Msg( "TFMM_MATCH_BEGIN_FAILED %s\n", args[1] );
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
