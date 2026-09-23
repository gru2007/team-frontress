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
//     the server can read and forward;
//   * and then keeps checking that the roster is still a way in. Publishing
//     the lobby once is not enough: a map load can exec a password back on
//     (which makes the stock code turn matchmaking mode off and takes the
//     gate down with it), and the reservation pass that turns a seat into an
//     admitted SteamID can decline and never be retried.
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
	virtual void FrameUpdatePreEntityThink() OVERRIDE;

	// Are we standing in for the GC on this server right now?
	bool BActive() const;

	// The coordinator handed us a match. Builds the lobby and publishes it,
	// which is what makes the stock server code treat this as a real match --
	// including changing to the map, which CTFGCServerSystem does for us.
	bool BeginMatch( uint64 ulMatchID, int nMatchGroup, const char *pszMap,
	                 const char *pszServerConfig, const char *pszFallbackPassword,
	                 const CUtlVector< TFMMSeat_t > &vecSeats, int nMaxPlayers );

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
	// Destroy the lobby object and create it again. Only a create produces the
	// SOCreated that builds CMatchInfo, so this is how a lobby that went in
	// with nobody listening gets a match record after the fact.
	bool BRepublishLobbyFromScratch();
	// Hold the door open. The roster only works as a gate for as long as the
	// server is still in matchmaking mode with no password -- and a map load
	// can quietly take both of those away. See the definition.
	void EnforceRosterGate();
	// The roster lives in one shared object. Make sure it is still in the
	// cache, and that the stock gate really does admit everybody sitting in
	// it -- asking the same function the engine asks on every connection.
	void VerifyAdmissions();
	// Apply the acknowledgement heartbeat the stock game server sends to its
	// GC. This is the step that turns an offered seat into one the strict
	// connection gate will actually admit.
	bool BApplyMatchmakingStatus( const CMsgGameServerMatchmakingStatus &msgStatus );
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
	// Set while BCreateFromMsg/BUpdateFromMsg are dispatching to listeners.
	// Publishing from inside that dispatch is what a second create -- read as
	// a second match -- would come from, so it is deferred to the next frame.
	bool        m_bPublishingLobby;
	bool        m_bLobbyPublishPending;
	bool        m_bWarnedPublishFailed;
	// The gate complaints are per-match and once each: they fire from a
	// per-frame check, so anything unrationed buries the console.
	bool        m_bWarnedPasswordReturned;
	bool        m_bWarnedGateDropped;
	bool        m_bWarnedAdmissionsStuck;
	// Latched once every seat has been admitted, so the good news is said
	// once rather than every couple of seconds.
	bool        m_bAdmissionsReported;
	float       m_flNextAdmissionCheck;
	int         m_nAdmissionNudges;
	// Separate budget from the seat nudges above: this one counts attempts to
	// make the server build a CMatchInfo at all, which is a different failure.
	int         m_nMatchRebuilds;
	// Non-zero while this match intentionally runs as a plain/password
	// fallback. There is no roster gate to update in that mode.
	uint64      m_ulPlainMatchID;
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
