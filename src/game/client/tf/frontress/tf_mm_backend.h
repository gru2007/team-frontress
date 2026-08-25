//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: Matchmaking without a Valve game coordinator.
//
// Team Fortress' matchmaking UI talks to a GC we do not have and cannot run.
// What it actually reads, though, is two shared objects in the local player's
// SO cache: CTFParty (who is in my party, what am I queued for) and CTFGSLobby
// (what match am I in, where do I connect). Everything else -- the dashboard,
// the playlist, the party panel, the "match found" popup -- is a view over
// those two objects.
//
// So this is not a reimplementation of the matchmaking UI. It is a small local
// game coordinator that owns those two objects and keeps them true:
//
//   * the party is a Steam lobby. Invites, joins, chat and membership are
//     Steam's, which is what Momentum Mod does and why it needs no backend of
//     its own for the social half of matchmaking;
//   * the queue is our Go coordinator over HTTP. It is the only part that has
//     to know about servers, and the only part a small community has to host;
//   * the messages the stock client sends towards the GC are answered here
//     instead of on the wire (see BHandleClientMsg and the hook in
//     tf_gc_shared.h).
//
//=============================================================================//

#ifndef TF_MM_BACKEND_H
#define TF_MM_BACKEND_H
#ifdef _WIN32
#pragma once
#endif

#include "igamesystem.h"
#include "steam/steam_api.h"
#include "gcsdk/gcclient_sharedobjectcache.h"
#include "tf_gcmessages.pb.h"
#include "utlstring.h"
#include "utlvector.h"

namespace google { namespace protobuf { class Message; } }
namespace GCSDK { class CWebAPIValues; }

// Party size the stock UI is built for. MAX_PARTY_SIZE lives in tf_party.h on
// the game side; the Steam lobby is created with the same ceiling.
static const int k_nTFMMMaxPartyMembers = 6;

//-----------------------------------------------------------------------------
// Where the local player is in the matchmaking flow.
//-----------------------------------------------------------------------------
enum ETFMMState
{
	k_eTFMMState_Idle,        // not looking for anything
	k_eTFMMState_Searching,   // in the coordinator's queue
	k_eTFMMState_MatchReady,  // the coordinator handed us a server
	k_eTFMMState_Connecting,  // we ran the connect command
	k_eTFMMState_InMatch,     // we are on the match server
};

//-----------------------------------------------------------------------------
// The party, which is a Steam lobby.
//
// A solo player has no lobby at all: the backend synthesizes a one-member party
// object for the UI, and the lobby is only created when somebody is actually
// invited. That keeps the common case free of Steam round-trips.
//-----------------------------------------------------------------------------
class CTFMMParty
{
public:
	CTFMMParty();

	void Create();                                  // start hosting a party lobby
	void Leave();
	bool BTryJoin( const CSteamID &lobbyID );
	bool BTryJoinFromString( const char *pszID );
	void OpenInviteDialog();

	// Who Steam should let into the lobby, from the player's own setting, and
	// pushing it to Steam when it changes.
	static ELobbyType WantedLobbyType();
	void ApplyJoinPolicy();

	bool BValid() const { return m_lobbyID.IsValid() && m_lobbyID.IsLobby(); }
	bool BIsLeader() const;
	CSteamID GetLobbyID() const { return m_lobbyID; }
	CSteamID GetLeader() const;
	uint64 GetPartyID() const;

	int GetNumMembers() const;
	CSteamID GetMember( int i ) const;

	// Lobby data the leader publishes for the whole party to read.
	void SetLobbyData( const char *pszKey, const char *pszValue );
	const char *GetLobbyData( const char *pszKey ) const;

	void SendChat( const char *pszText );

	// Steam rich presence: what the friends list shows, and what "Join Game"
	// actually does. Called whenever the lobby changes.
	void UpdateRichPresence() const;

	// The lobby a friend is in, from their rich presence, or nil. This is how
	// a join request finds a party without a coordinator in the middle.
	static CSteamID GetFriendPartyLobby( const CSteamID &friendID );

	// The SourceTV relay of the match a friend is in, from their rich
	// presence, or NULL. Owned by Steam; copy it if you keep it.
	static const char *GetFriendSTV( const CSteamID &friendID );

	// The connect string Steam should use for us, or NULL if we have no party
	// worth joining. Owned by the party; copy it if you keep it.
	const char *GetJoinConnectString() const;

private:
	void OnCreated( LobbyCreated_t *pCreated, bool bIOFailure );
	void OnJoined( LobbyEnter_t *pEntered, bool bIOFailure );

	STEAM_CALLBACK( CTFMMParty, OnLobbyEnter, LobbyEnter_t );
	STEAM_CALLBACK( CTFMMParty, OnLobbyChatUpdate, LobbyChatUpdate_t );
	STEAM_CALLBACK( CTFMMParty, OnLobbyDataUpdate, LobbyDataUpdate_t );
	STEAM_CALLBACK( CTFMMParty, OnLobbyChatMsg, LobbyChatMsg_t );
	STEAM_CALLBACK( CTFMMParty, OnJoinRequested, GameLobbyJoinRequested_t );

	CSteamID m_lobbyID;
	bool     m_bHosting;
	// The lobby type we last told Steam, so an unchanged setting is not
	// re-sent every frame.
	ELobbyType m_eAppliedLobbyType;
	mutable char m_szConnectString[64];

	CCallResult< CTFMMParty, LobbyCreated_t > m_callLobbyCreated;
	CCallResult< CTFMMParty, LobbyEnter_t >   m_callLobbyJoined;
};

//-----------------------------------------------------------------------------
// The coordinator link. One request in flight at a time, over Steam's HTTP so
// we inherit its proxy and certificate handling.
//-----------------------------------------------------------------------------
class CTFMMCoordinator
{
public:
	CTFMMCoordinator();
	~CTFMMCoordinator();

	// Callback receives the parsed JSON body, or NULL if the request failed.
	// The values are owned by the caller of the callback and freed after it
	// returns; copy anything you keep.
	typedef void (*FnResponse)( GCSDK::CWebAPIValues *pValues, int eStatusCode, void *pContext );

	bool BSend( EHTTPMethod eMethod, const char *pszPath, const char *pszBody,
	            FnResponse pfnCallback, void *pContext );

	bool BBusy() const { return m_hRequest != INVALID_HTTPREQUEST_HANDLE; }
	void Cancel();

	// Hex-encoded Steam web API auth ticket for this session. Empty until
	// Steam answers -- the request is asynchronous -- and empty for good when
	// Steam will not issue one.
	const char *GetAuthTicket();

	// Start minting a ticket now, so it is there when somebody presses play.
	void RequestAuthTicket();

	// Has Steam given us a ticket yet? A coordinator in webapi mode refuses a
	// queue request without one, so the queue waits rather than being refused.
	bool BHaveAuthTicket() const { return !m_strTicket.IsEmpty(); }

private:
	void OnRequestCompleted( HTTPRequestCompleted_t *pInfo, bool bIOFailure );

	STEAM_CALLBACK( CTFMMCoordinator, OnWebApiTicket, GetTicketForWebApiResponse_t );

	HTTPRequestHandle m_hRequest;
	FnResponse        m_pfnCallback;
	void             *m_pContext;
	CCallResult< CTFMMCoordinator, HTTPRequestCompleted_t > m_callCompleted;

	CUtlString      m_strTicket;
	HAuthTicket     m_hAuthTicket;
};

//-----------------------------------------------------------------------------
// The local game coordinator.
//-----------------------------------------------------------------------------
class CTFMMBackend : public CAutoGameSystemPerFrame, public GCSDK::ISharedObjectListener
{
public:
	CTFMMBackend();

	// CAutoGameSystemPerFrame
	virtual bool Init() OVERRIDE;
	virtual void Shutdown() OVERRIDE;
	virtual void Update( float frametime ) OVERRIDE;

	// ISharedObjectListener. We only care that the cache came back, so we can
	// re-publish our objects into it.
	virtual void SOCreated( const CSteamID &, const GCSDK::CSharedObject *, GCSDK::ESOCacheEvent ) OVERRIDE {}
	virtual void PreSOUpdate( const CSteamID &, GCSDK::ESOCacheEvent ) OVERRIDE {}
	virtual void SOUpdated( const CSteamID &, const GCSDK::CSharedObject *, GCSDK::ESOCacheEvent ) OVERRIDE {}
	virtual void PostSOUpdate( const CSteamID &, GCSDK::ESOCacheEvent ) OVERRIDE {}
	virtual void SODestroyed( const CSteamID &, const GCSDK::CSharedObject *, GCSDK::ESOCacheEvent ) OVERRIDE {}
	virtual void SOCacheSubscribed( const CSteamID &steamIDOwner, GCSDK::ESOCacheEvent eEvent ) OVERRIDE;
	virtual void SOCacheUnsubscribed( const CSteamID &steamIDOwner, GCSDK::ESOCacheEvent eEvent ) OVERRIDE;

	// Is the backend the thing answering for the GC right now?
	bool BActive() const;

	// Answer a message the stock client would have sent to the GC. Returns
	// false if we do not know the message, in which case the caller keeps
	// waiting for a GC that is not coming -- so every message the client can
	// send should be answered here, even if the answer is "nothing happened".
	// pMsgReply is NULL for the fire-and-forget messages, which have no reply
	// type at all.
	bool BHandleClientMsg( uint32 unMsgType, const ::google::protobuf::Message &msgRequest,
	                       ::google::protobuf::Message *pMsgReply );

	ETFMMState GetState() const { return m_eState; }
	CTFMMParty &Party() { return m_party; }
	const char *GetSTVAddress() const { return m_strSTV.Get(); }

	// What the coordinator last said about the queue we are standing in. The
	// menu shows this; nothing in matchmaking depends on it.
	ETFMatchGroup GetQueuedMatchGroup() const { return m_eQueuedMatchGroup; }
	int   GetQueuePlayerCount() const { return m_nInQueue; }
	int   GetQueueNeededCount() const { return m_nNeedPlayers; }
	float GetQueueSeconds() const;
	const char *GetLastError() const { return m_strLastError.Get(); }

	// What the coordinator remembers about us: matches, XP, level. Polled with
	// the status feed. bValid is false until it has answered once.
	struct Progress_t
	{
		Progress_t()
			: bValid( false ), nMatches( 0 ), nWins( 0 ), nLosses( 0 )
			, nAbandons( 0 ), nXP( 0 ), nLevel( 1 ), nLevelXP( 0 ), nLevelXPTotal( 0 )
		{}
		bool bValid;
		int  nMatches;
		int  nWins;
		int  nLosses;
		int  nAbandons;
		int  nXP;
		int  nLevel;
		int  nLevelXP;
		int  nLevelXPTotal;
	};
	const Progress_t &GetProgress() const { return m_progress; }

	// Ask the coordinator for our record now, rather than waiting for the next
	// status poll. Called when a match ends, which is the only time it changes.
	void RefreshProgress();

	// The coordinator's public health view, polled while we sit at the menu.
	// bValid is false until the first reply lands, and stays false when the
	// coordinator cannot be reached.
	struct Status_t
	{
		Status_t()
			: bChecked( false )
			, bValid( false )
			, bServerCapacityKnown( false )
			, nOnlinePlayers( 0 )
			, nLiveMatches( 0 )
			, nFreeServers( 0 )
		{}
		// bChecked separates "we have not asked yet" from "we asked and it did
		// not answer", which are different things to put in front of a player.
		bool       bChecked;
		bool       bValid;
		bool       bServerCapacityKnown;
		int        nOnlinePlayers;
		int        nLiveMatches;
		int        nFreeServers;
		CUtlString strName;
	};
	const Status_t &GetStatus() const { return m_status; }

	// What the coordinator says a match group actually plays, from the same
	// status poll. NULL until it has said so -- which is not the same thing as
	// a group with no maps. The menu shows a map list the player picks from,
	// and a map nobody here runs has no business being in it.
	const CUtlVector< CUtlString > *GetGroupMaps( ETFMatchGroup eMatchGroup ) const;

	// Does the coordinator run this match group at all? True while we have not
	// heard from it: the menu should not gate on an answer nobody gave.
	bool BGroupOffered( ETFMatchGroup eMatchGroup ) const;

	// Does the coordinator serve anything at all? A status reply that offers
	// nothing is far more often an answer we failed to read than a service
	// with every mode switched off, and either way a menu with no modes on it
	// tells the player nothing.
	bool BAnyGroupOffered() const;

	// Has the coordinator actually listed its match groups yet? BGroupOffered
	// answers "yes" before it has, which is the right default for enabling a
	// button and the wrong one for taking a mode off the menu.
	bool BGroupsKnown() const { return m_bGroupsKnown; }

	// What the coordinator says about a queue that is not moving, e.g. a match
	// that formed and is waiting for a free server. Empty when there is
	// nothing to add to the state.
	const char *GetQueueDetail() const { return m_strQueueDetail.Get(); }

	// Bumped whenever the pools or the offered groups actually change. The map
	// list is built long before the first status poll answers, so it needs to
	// know when the answer changed under it.
	uint32 GetMapPoolGeneration() const { return m_nMapPoolGeneration; }

	// A party member noticed the leader's assignment in the lobby data.
	void OnPartyLobbyDataChanged();

	// We just entered somebody's party lobby. Joining a party is always a
	// deliberate act -- an invite accepted, a friend joined, connect_lobby --
	// and it means "put me where they are", so it arms a one-shot follow onto
	// whatever server the leader is on. Without it "Join Game" on a friend
	// playing a community server put you in their party and nowhere else.
	void OnPartyLobbyEntered();

	// Console-command surface, also used by the UI patches.
	void QueueForMatch( ETFMatchGroup eMatchGroup );
	// Ask to be seated in the match our party is already in, rather than
	// queued for a new one.
	void QueueForStandby();
	void CancelQueue();
	void JoinAssignedMatch();
	void Spew() const;

private:
	// SO plumbing
	// Tell the party which server we are on, so members can follow us there.
	void PublishPartyServer( bool bInGame );
	// Spend the one-shot follow, if it is armed and the leader is somewhere.
	void FollowLeaderToServer();
	GCSDK::CGCClientSharedObjectCache *GetLocalCache( bool bCreate );
	bool BEnsureCacheSubscribed();
	void PublishParty();
	void PublishLobby();
	void DestroyLobbySO();
	void RebuildPartyProto();

	// Coordinator conversation
	void SendQueueRequest();

	// The players a queue request stands for. Shared by the request and by
	// the check for somebody leaving the party mid-queue, so the two cannot
	// disagree about who was in the ticket.
	void BuildRoster( CUtlVector< CSteamID > &vecOut ) const;
	bool BRosterMatches( const CUtlVector< CSteamID > &vecNow ) const;
	void SendQueueCancel();
	void PollQueue();
	void PollStatus();
	void OnStatus( GCSDK::CWebAPIValues *pValues, int eStatusCode );
	static void StatusThunk( GCSDK::CWebAPIValues *pValues, int eStatusCode, void *pContext );
	void OnProgress( GCSDK::CWebAPIValues *pValues, int eStatusCode );
	static void ProgressThunk( GCSDK::CWebAPIValues *pValues, int eStatusCode, void *pContext );
	// Put the XP where the stock rank panel reads it: a CTFRatingData object
	// per display rating type, in our own SO cache.
	void PublishRatings();
	void OnQueueReply( GCSDK::CWebAPIValues *pValues, int eStatusCode );
	void OnQueueStatus( GCSDK::CWebAPIValues *pValues, int eStatusCode );
	static void QueueReplyThunk( GCSDK::CWebAPIValues *pValues, int eStatusCode, void *pContext );
	static void QueueStatusThunk( GCSDK::CWebAPIValues *pValues, int eStatusCode, void *pContext );

	// Take an assignment that arrived by some route other than our own queue
	// poll -- currently, the party leader publishing it into the lobby.
	void AdoptAssignment( const char *pszMatchID, const char *pszConnect, const char *pszPassword,
	                      const char *pszSTV, const char *pszTeams, ETFMatchGroup eMatchGroup );

	// Is the assignment we adopted one we actually have a seat in? A party
	// member who joined after the match formed does not.
	bool BSeatedInAssignment() const;

	void EnterState( ETFMMState eState );
	void Fail( const char *pszReason );
	void CollectSelectedMaps( CUtlVector< CUtlString > &vecOut ) const;

	CTFMMParty       m_party;
	CTFMMCoordinator m_coordinator;
	// Its own connection, so a slow status poll never delays a queue poll.
	CTFMMCoordinator m_statusFeed;

	ETFMMState m_eState;
	bool       m_bSubscribedToCache;
	// Publishing is retried every frame, so the complaint has to be rationed
	// or it buries everything else in the console.
	bool       m_bWarnedPublishFailed;
	bool       m_bPartyPublished;
	bool       m_bLobbyPublished;

	ETFMatchGroup m_eQueuedMatchGroup;
	CUtlString    m_strTicketID;
	// Set while the request in flight is a standby one: which running match we
	// are asking to be let into. Empty for an ordinary queue.
	CUtlString    m_strStandbyMatchID;
	CUtlString    m_strLastError;
	float         m_flNextPollTime;
	float         m_flQueueStartTime;
	// When we ran the connect command, so a connect that never lands can be
	// given up on instead of holding the match open forever.
	float         m_flConnectStartTime;
	// Whether the server we were on when the connect started has been left
	// yet. Until it has, being in a level says nothing about having arrived.
	bool          m_bConnectLeftOldServer;

	// Armed by entering a party lobby, spent on the first server the leader
	// turns out to be on. One-shot on purpose: a party member should not be
	// dragged across the internet every time the leader changes servers.
	bool          m_bFollowLeaderServer;
	// The last address we told the party we were on, so the lobby data is only
	// written when it actually changes.
	CUtlString    m_strPublishedServer;
	int           m_nPollIntervalMS;
	int           m_nInQueue;
	int           m_nNeedPlayers;

	// Snapshot used to detect somebody disappearing from the Steam lobby
	// after the queue ticket was created. The coordinator's ticket stores
	// a roster, so polling the same ticket cannot discover a departed member.
	CUtlVector< CSteamID > m_vecQueuedRoster;

	// The coordinator's own word on a stalled queue, from the last poll.
	CUtlString    m_strQueueDetail;

	Status_t      m_status;
	float         m_flNextStatusPoll;
	Progress_t    m_progress;
	float         m_flNextProgressPoll;
	// The rating objects we have already put in the cache, so they are updated
	// rather than created a second time.
	CUtlVector< int > m_vecPublishedRatingTypes;

	// Indexed by ETFMatchGroup, which is a small dense enum.
	static const int k_nMatchGroupPools = k_eTFMatchGroup_Event_Last + 1;
	CUtlVector< CUtlString > m_arGroupMaps[ k_nMatchGroupPools ];
	bool                     m_arGroupOffered[ k_nMatchGroupPools ];
	bool                     m_bGroupsKnown;
	CUtlString               m_strGroupSignature;
	uint32                   m_nMapPoolGeneration;

	CSOTFParty             m_msgParty;
	std::string            m_strLastPublishedParty;
	CSOTFGameServerLobby   m_msgLobby;
	CUtlString             m_strConnect;
	CUtlString             m_strPassword;
	CUtlString             m_strSTV;
	CUtlString             m_strMatchID;
};

CTFMMBackend *TFMMBackend();

// True when the pool contains the map. Case-insensitive: map names come from a
// config file somebody types by hand.
bool BMapInPool( const CUtlVector< CUtlString > &vecPool, const char *pszMap );

#endif // TF_MM_BACKEND_H
