//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: What the main menu shows about the game coordinator.
//
// Matchmaking itself is the stock party client talking to our GC. The menu
// additionally shows things the stock UI has no place for -- how many people
// are online, how full a queue is, which maps a mode really runs -- and that
// comes from the GC's public status (GET /v1/status) plus the party and lobby
// objects already in our shared object cache. Read-only: nothing here sends a
// matchmaking request or decides anything.
//
//=============================================================================//

#ifndef TF_GC_STATUS_H
#define TF_GC_STATUS_H
#ifdef _WIN32
#pragma once
#endif

#include "igamesystem.h"
#include "steam/steam_api.h"
#include "tf_gcmessages.pb.h"
#include "utlstring.h"
#include "utlvector.h"

// Where the local player is in the matchmaking flow, as the menu words it.
enum ETFMMState
{
	k_eTFMMState_Idle,        // not looking for anything
	k_eTFMMState_Searching,   // in a queue
	k_eTFMMState_MatchReady,  // a match formed; its server is getting ready or ready
	k_eTFMMState_Connecting,  // joining the match server
	k_eTFMMState_InMatch,     // on the match server
};

class CTFGCStatus : public CAutoGameSystemPerFrame
{
public:
	CTFGCStatus();

	virtual void Update( float frametime ) OVERRIDE;
	virtual void Shutdown() OVERRIDE;

	// Is the game coordinator connected?
	bool BActive() const;

	struct Status_t
	{
		Status_t()
			: bChecked( false ), bValid( false ), bServerCapacityKnown( false )
			, nOnlinePlayers( 0 ), nLiveMatches( 0 ), nFreeServers( 0 )
		{}
		bool       bChecked;              // we have asked at least once
		bool       bValid;                // and the last answer was good
		bool       bServerCapacityKnown;  // free servers is a real number (no on-demand provider)
		int        nOnlinePlayers;
		int        nLiveMatches;
		int        nFreeServers;
		CUtlString strName;
	};
	const Status_t &GetStatus() const { return m_status; }

	ETFMMState GetState() const;
	ETFMatchGroup GetQueuedMatchGroup() const;
	int   GetQueuePlayerCount() const;
	int   GetQueueNeededCount() const;
	float GetQueueSeconds() const;
	// A line about a queue that is not moving, e.g. a formed match waiting
	// for a server. Empty when there is nothing to add.
	const char *GetQueueDetail() const;

	// The maps a group really runs, or NULL until the GC has said.
	const CUtlVector< CUtlString > *GetGroupMaps( ETFMatchGroup eMatchGroup ) const;
	// Does the GC run this group? True until it has said otherwise.
	bool BGroupOffered( ETFMatchGroup eMatchGroup ) const;
	bool BAnyGroupOffered() const;
	bool BGroupsKnown() const { return m_bGroupsKnown; }
	// Bumped when the offered groups or their maps change.
	uint32 GetMapPoolGeneration() const { return m_nMapPoolGeneration; }

private:
	void Poll();
	void OnStatus( HTTPRequestCompleted_t *pInfo, bool bIOFailure );

	static const int k_nGroups = k_eTFMatchGroup_Event_Last + 1;
	struct Group_t
	{
		Group_t() : bOffered( false ), nQueued( 0 ), nNeed( 0 ) {}
		bool bOffered;
		int  nQueued;
		int  nNeed;
		CUtlVector< CUtlString > vecMaps;
	};

	Status_t   m_status;
	Group_t    m_groups[ k_nGroups ];
	bool       m_bGroupsKnown;
	CUtlString m_strSignature;
	uint32     m_nMapPoolGeneration;
	float      m_flNextPoll;

	HTTPRequestHandle m_hRequest;
	CCallResult< CTFGCStatus, HTTPRequestCompleted_t > m_callStatus;
};

CTFGCStatus *TFGCStatus();

// True when the pool contains the map, ignoring case.
bool BMapInPool( const CUtlVector< CUtlString > &vecPool, const char *pszMap );

#endif // TF_GC_STATUS_H
