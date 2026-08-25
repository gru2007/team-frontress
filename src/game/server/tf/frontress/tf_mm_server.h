//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The other half of matchmaking without a Valve game coordinator.
//
// The client half (src/game/client/tf/frontress/) owns CTFParty and CTFGSLobby
// in the *player's* shared object cache, which is what the matchmaking UI reads.
// This owns CTFGSLobby in the *game server's* cache, which is what everything
// server-side reads:
//
//   CTFGSLobby  ->  CTFGCServerSystem::SOCreated  ->  CMatchInfo
//
// and CMatchInfo is the thing the server asks about on every interesting
// question. Without it GetCurrentMatchGroup() is Invalid, SteamIDAllowedToConnect
// lets anybody in, teams are advisory, abandons are not recorded, there is no
// match summary and no result to report. With it, all of that is stock Valve
// code doing what it always did.
//
// So this is deliberately small. It does two things:
//
//   * builds a lobby object from what the coordinator tells it over RCON and
//     publishes it into the local cache (the same AddLocalSOCache trick the
//     client uses, for the same reason -- nothing else can subscribe a cache
//     with no GC on the other end);
//   * answers the messages the server would have sent to the GC, so the
//     reliable message queue never wedges waiting for a reply that is not
//     coming. Anything it answers that carries information out -- the match
//     result, a player leaving -- it prints in a form the log agent next to
//     the server can read and forward.
//
//=============================================================================//

#ifndef TF_MM_SERVER_H
#define TF_MM_SERVER_H
#ifdef _WIN32
#pragma once
#endif

#include "igamesystem.h"
#include "tf_gcmessages.pb.h"
#include "utlstring.h"
#include "utlvector.h"

namespace google { namespace protobuf { class Message; } }

//-----------------------------------------------------------------------------
// One seat the coordinator sold.
//-----------------------------------------------------------------------------
struct TFMMSeat_t
{
	uint64 ulSteamID;
	int    nGCTeam;   // TF_GC_TEAM
	CUtlString strName;
};

//-----------------------------------------------------------------------------
class CTFMMServer : public CAutoGameSystemPerFrame
{
public:
	CTFMMServer();

	virtual bool Init() OVERRIDE;
	virtual void Shutdown() OVERRIDE;
	virtual void Update( float frametime ) OVERRIDE;

	// Are we standing in for the GC on this server right now?
	bool BActive() const;

	// The coordinator handed us a match. Builds the lobby and publishes it,
	// which is what makes the stock server code treat this as a real match --
	// including changing to the map, which CTFGCServerSystem does for us.
	void BeginMatch( uint64 ulMatchID, int nMatchGroup, const char *pszMap,
	                 const char *pszServerConfig, const char *pszFallbackPassword,
	                 const CUtlVector< TFMMSeat_t > &vecSeats );

	// Sell more seats in the match that is already running. This is how
	// backfill and standby get past the roster gate: the gate is the lobby, so
	// joining a running match means being added to it first.
	//
	// Returns how many seats were actually new.
	int AddSeats( uint64 ulMatchID, const CUtlVector< TFMMSeat_t > &vecSeats );

	// The match is over, or the coordinator took the server back.
	void EndMatch( const char *pszWhy );

	bool BHaveMatch() const { return m_bPublished; }
	uint64 GetMatchID() const { return m_msgLobby.match_id(); }

	// Answer a message the server would have sent to the GC. Same contract as
	// the client's BHandleClientMsg: false means "not ours", and the caller
	// goes on waiting for a GC that is not there -- so every message the
	// server can send should be answered here even when the answer is nothing.
	bool BHandleServerMsg( uint32 unMsgType, const ::google::protobuf::Message &msgRequest,
	                       ::google::protobuf::Message *pMsgReply );

	void Spew() const;

private:
	bool BPublishLobby();
	// The map change the lobby would have done, when there is no lobby. The
	// coordinator does not do it itself, so somebody has to.
	void FallBackToPlainMatch( const char *pszMap );
	bool BEnsureCacheSubscribed();
	void DestroyLobby();
	CSteamID OurSteamID() const;

	// Report a line the log agent next to us picks up, through the server log
	// -- which is what logaddress_add forwards. One place, so the format
	// cannot drift between the things that emit it.
	void ReportLine( const char *pszEvent, const char *pszBody ) const;

	CSOTFGameServerLobby m_msgLobby;
	bool        m_bPublished;
	bool        m_bWarnedPublishFailed;
	// Set while we are waiting for the map change the lobby triggered, so the
	// lobby can be flipped from SERVERSETUP to RUN once we are on it.
	bool        m_bAwaitingMap;
	CUtlString  m_strMap;
	CUtlString  m_strLastPublished;
	// The password to put back if the roster gate cannot be raised. Taking the
	// password off is only safe while something better is in its place.
	CUtlString  m_strFallbackPassword;
	// The ruleset this match wants exec'd after the map loads. It has to be
	// re-applied after the lobby is published, because publishing is what runs
	// the match group's own InitServerSettingsForMatch and that overwrites it.
	CUtlString  m_strServerConfig;
};

CTFMMServer *TFMMServer();

#endif // TF_MM_SERVER_H
