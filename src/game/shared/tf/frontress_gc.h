//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The connection to the Team Frontress game coordinator.
//
// Valve's gcsdk -- the library CGCClientSystem is built on -- still contains
// the whole client half of the GC protocol: shared object caches, jobs, the
// reliable message queue. What the public SDK took out is the pipe: Steam's
// ISteamGameCoordinator, through which gcsdk sends every message and from
// which Steam delivered every reply.
//
// This is that pipe, pointed at our own GC (services/gc). It implements
// ISteamGameCoordinator for gcsdk's outbound path, and feeds the GC's messages
// back into gcsdk's job manager exactly as Steam's callback used to. Above it,
// nothing is emulated: the party UI, the matchmaking dashboard, the match
// found dialog, CTFGCServerSystem and CMatchInfo all talk to a real GC.
//
// The wire is three HTTP calls over ISteamHTTP (see services/gc/internal/httpapi):
// a session, batched sends, and a long-poll receive. Every message is
// numbered in both directions, so a lost or repeated HTTP request never loses
// or repeats a message.
//
//=============================================================================//

#ifndef FRONTRESS_GC_H
#define FRONTRESS_GC_H
#ifdef _WIN32
#pragma once
#endif

#include "steam/steam_api.h"
#include "utlvector.h"
#include "utlstring.h"

// The interface gcsdk's CGCClient sends through. The public Steamworks SDK no
// longer ships isteamgamecoordinator.h; the prebuilt gcsdk still calls these
// three virtuals, in this order, so the declaration has to match it exactly.
enum EGCResults
{
	k_EGCResultOK = 0,
	k_EGCResultNoMessage = 1,
	k_EGCResultBufferTooSmall = 2,
	k_EGCResultNotLoggedOn = 3,
	k_EGCResultInvalidMessage = 4,
};

class ISteamGameCoordinator
{
public:
	virtual EGCResults SendGCMessage( uint32 unMsgType, const void *pubData, uint32 cubData ) = 0;
	virtual bool IsMessageAvailable( uint32 *pcubMsgSize ) = 0;
	virtual EGCResults RetrieveMessage( uint32 *punMsgType, void *pubDest, uint32 cubDest, uint32 *pcubMsgSize ) = 0;
};

//-----------------------------------------------------------------------------
class CFrontressGC : public ISteamGameCoordinator
{
public:
	CFrontressGC();
	~CFrontressGC();

	// ISteamGameCoordinator. gcsdk calls SendGCMessage; the other two exist
	// for the vtable -- messages are pushed into gcsdk, never pulled.
	virtual EGCResults SendGCMessage( uint32 unMsgType, const void *pubData, uint32 cubData ) OVERRIDE;
	virtual bool IsMessageAvailable( uint32 *pcubMsgSize ) OVERRIDE { if ( pcubMsgSize ) *pcubMsgSize = 0; return false; }
	virtual EGCResults RetrieveMessage( uint32 *, void *, uint32, uint32 *pcubMsgSize ) OVERRIDE { if ( pcubMsgSize ) *pcubMsgSize = 0; return k_EGCResultNoMessage; }

	// Once a frame, from CGCClientSystem.
	void Think();
	void Shutdown();

	// Is there a live session? This is what BConnectedtoGC() reports.
	bool BConnected() const { return m_eState == k_eState_Connected; }

	// Ask the GC to resend every object it keeps in a cache, because something
	// replaced that cache wholesale (the web API inventory does).
	void RequestCacheRefresh( const CSteamID &owner );

	// What is happening, for tf_gc_status and the menu.
	const char *GetStateName() const;
	const char *GetLastError() const { return m_strLastError.Get(); }
	const char *GetBaseURL() const;

private:
	enum EState
	{
		k_eState_Idle,            // not configured, or Steam not logged on
		k_eState_WaitingTicket,   // client: Steam is minting a web API ticket
		k_eState_Starting,        // the session request is in flight
		k_eState_Connected,
		k_eState_Backoff,         // something failed; trying again soon
	};

	struct Frame_t
	{
		uint64 m_ulSeq;
		uint32 m_unType;
		CUtlVector< uint8 > m_data;
	};

	bool BWantSession() const;
	CSteamID OurSteamID() const;
	void StartSession();
	void LoseSession( const char *pszWhy );
	void Backoff( const char *pszWhy, float flSeconds );
	void FlushSends();
	void StartRecv();
	void Dispatch( const uint8 *pubData, uint32 cubData );
	void EnsureOwnCacheSubscribed();

	ISteamHTTP *HTTP() const;
	HTTPRequestHandle CreateRequest( EHTTPMethod eMethod, const char *pszPath );

	void OnSessionCompleted( HTTPRequestCompleted_t *pInfo, bool bIOFailure );
	void OnSendCompleted( HTTPRequestCompleted_t *pInfo, bool bIOFailure );
	void OnRecvCompleted( HTTPRequestCompleted_t *pInfo, bool bIOFailure );

#ifdef CLIENT_DLL
	STEAM_CALLBACK( CFrontressGC, OnWebApiTicket, GetTicketForWebApiResponse_t );
	HAuthTicket m_hTicket;
	CUtlString  m_strTicket;
#endif

	EState      m_eState;
	float       m_flNextAttempt;
	float       m_flBackoff;
	CUtlString  m_strSession;
	CUtlString  m_strLastError;
	uint32      m_unPollMS;

	CUtlVector< Frame_t * > m_vecOutbox;
	uint64      m_ulNextOutSeq;
	uint64      m_ulSendUpTo;   // highest seq in the send in flight
	uint64      m_ulLastInSeq;  // highest seq received and dispatched
	float       m_flNextRecv;
	float       m_flNextRefresh;

	HTTPRequestHandle m_hSession;
	HTTPRequestHandle m_hSend;
	HTTPRequestHandle m_hRecv;
	CCallResult< CFrontressGC, HTTPRequestCompleted_t > m_callSession;
	CCallResult< CFrontressGC, HTTPRequestCompleted_t > m_callSend;
	CCallResult< CFrontressGC, HTTPRequestCompleted_t > m_callRecv;
};

CFrontressGC *FrontressGC();

#endif // FRONTRESS_GC_H
