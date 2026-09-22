//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: Backend for the main menu's matchmaking status (see
//          tf_mainmenu_info.cpp / tf_campaign_map.cpp -- both already call
//          into this interface, they just needed it to actually exist).
//
//          Everything that reflects *this client's own* matchmaking state --
//          queue membership, match group, lobby readiness -- is read straight
//          from the real, untouched Valve GC objects: CTFPartyClient,
//          CTFGCClientSystem and the CTFGSLobby shared object. There is no
//          separate client-side state machine to keep in sync with the GC;
//          GetState() just re-derives itself from those objects every time
//          it's asked.
//
//          The one thing no single client's GC session can see -- aggregate
//          population/queue-depth/server-capacity numbers across everyone --
//          comes from a plain, unauthenticated HTTP GET against the
//          coordinator's public status endpoint (GET {tf_gc_address}/v1/status,
//          see frontress_gc.cpp for the address convar and the request pattern
//          this mirrors).
//
//=============================================================================//

#ifndef TF_MM_BACKEND_H
#define TF_MM_BACKEND_H
#ifdef _WIN32
#pragma once
#endif

#include "tf_matchmaking_shared.h"

class CTFMMStatusRequest;

//-----------------------------------------------------------------------------
// Purpose: High level matchmaking state shown on the main menu / campaign map.
//-----------------------------------------------------------------------------
enum ETFMMState
{
	k_eTFMMState_Idle,			// Not queued, no assigned match.
	k_eTFMMState_Searching,		// Queued (or in the party's standby queue) for a match group.
	k_eTFMMState_MatchReady,	// GC lobby exists and is standing up / has a server for us.
	k_eTFMMState_Connecting,	// Lobby has a connect string and we haven't joined it yet.
	k_eTFMMState_InMatch,		// We're actually connected to our live match server.
};

//-----------------------------------------------------------------------------
// Purpose: Read-only facade over the real GC matchmaking state plus the
//          coordinator's public status feed. One lazily-created instance,
//          always used through the const global accessor TFMMBackend().
//-----------------------------------------------------------------------------
class CTFMMBackend
{
public:
	CTFMMBackend();
	~CTFMMBackend();

	// Where we are in the matchmaking flow right now. Derived live from
	// CTFPartyClient / CTFGCClientSystem / CTFGSLobby -- never stale by more
	// than the caller's own polling cadence (both call sites poll every frame).
	ETFMMState GetState() const;

	// Which match group we're queued for, k_eTFMatchGroup_Invalid if none.
	ETFMatchGroup GetQueuedMatchGroup() const;

	// How long (seconds) we've been continuously queued for GetQueuedMatchGroup().
	float GetQueueSeconds() const;

	// Coordinator-reported queue depth for our queued match group. Both 0 if
	// we aren't queued, or if the coordinator hasn't answered a status poll yet.
	int GetQueuePlayerCount() const;
	int GetQueueNeededCount() const;

	// Optional extra line describing the search -- a real Valve matchmaker
	// health-bracket localization token (e.g. "#TF_Casual_QueueEstimation_Good",
	// see CTFGCClientSystem::GetOverallHealthDataForLocalCriteria()). NULL if
	// we're not searching or there's nothing to say.
	const char *GetQueueDetail() const;

	// Coordinator population/health snapshot, from GET {tf_gc_address}/v1/status.
	struct Status_t
	{
		Status_t() : bChecked( false ), bValid( false ), bServerCapacityKnown( false ),
			nOnlinePlayers( 0 ), nLiveMatches( 0 ), nFreeServers( 0 ) {}

		bool bChecked;				// Have we ever gotten a response back (success or failure)?
		bool bValid;				// Was the most recent poll a successful 200 OK w/ parseable JSON?
		bool bServerCapacityKnown;
		CUtlString strName;
		int nOnlinePlayers;
		int nLiveMatches;
		int nFreeServers;
	};
	const Status_t &GetStatus() const;

private:
	friend class CTFMMStatusRequest;

	struct QueueGroupInfo_t
	{
		ETFMatchGroup eMatchGroup;
		int nQueued;
		int nMinPlayers;
	};

	// Re-derives the cached state/queued-group/queue-timing from the real GC
	// objects, and kicks a new status poll if one is due. Idempotent within
	// a frame; every public accessor calls this first so callers can query
	// the getters in any order.
	void EnsureFresh() const;
	void RefreshQueueTiming() const;
	void PollStatusIfDue() const;

	// Called back by CTFMMStatusRequest once its HTTP GET resolves.
	void OnStatusReceived( CUtlBuffer &bufResponse );
	void OnStatusRequestFinished( CTFMMStatusRequest *pRequest, bool bSuccess );

	mutable double m_flLastRefresh;

	mutable ETFMMState m_eCachedState;
	mutable ETFMatchGroup m_eCachedQueuedGroup;

	mutable ETFMatchGroup m_eQueueTrackedGroup;
	mutable double m_flQueueStartTime;

	mutable CUtlString m_strQueueDetail;

	mutable Status_t m_Status;
	mutable CUtlVector< QueueGroupInfo_t > m_vecQueueGroups;
	mutable double m_flNextStatusPoll;
	mutable CTFMMStatusRequest *m_pPendingStatusRequest;
};

// Global accessor. Lazily constructs the single backend instance on first use.
const CTFMMBackend *TFMMBackend();

#endif // TF_MM_BACKEND_H
