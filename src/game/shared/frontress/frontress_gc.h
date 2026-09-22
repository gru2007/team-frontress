//========= Copyright Team Frontress, All rights reserved. ===================//
//
// Purpose: The transport that replaces Valve's Game Coordinator.
//
//          Team Comtress ships a gcsdk_sdk library whose transport is gutted:
//          CGCClient::BInit() ignores its ISteamGameCoordinator, BSendMessage()
//          returns false and OnGCMessageAvailable() is an empty function. So
//          nothing the game sends ever leaves, and nothing ever arrives -- the
//          whole party/lobby/match machinery above it is intact and starved.
//
//          This puts a transport back underneath it, pointed at our own
//          coordinator instead of Steam. It is deliberately the only thing
//          that changes: messages are the same GC protobufs, the shared object
//          caches are filled by the same jobs the library already registers
//          (CGCSOCacheSubscribedJob and friends), and CTFPartyClient,
//          CTFGCClientSystem and CTFGCServerSystem are untouched. From the
//          game's point of view a GC came back.
//
//          What does NOT come through here is the item inventory. That still
//          goes to Steam over the WebAPI path in tf_gc_client.cpp, because the
//          items are real Steam items and we are not in the business of
//          inventing them.
//
//=============================================================================//

#ifndef FRONTRESS_GC_H
#define FRONTRESS_GC_H
#ifdef _WIN32
#pragma once
#endif

#include "gcsdk/msgprotobuf.h"
#include "steam/steam_api.h"
#include "utlbuffer.h"
#include "utlstring.h"
#include "utlvector.h"

//-----------------------------------------------------------------------------
// One HTTP exchange with the coordinator carries a batch of GC messages in
// each direction. The framing is deliberately trivial, because the messages
// inside it are already the GC's own framing and that is the part that has to
// be right:
//
//   uint32  magic  'F','G','C','1'
//   uint32  count
//   count x { uint32 cubMsg; uint8 rgubMsg[ cubMsg ] }
//
// Each rgubMsg is exactly what GCSDK hands us: ProtoBufMsgHeader_t, then the
// serialized CMsgProtoBufHeader, then the body. Little endian, like the rest
// of the engine.
//-----------------------------------------------------------------------------
#define FRONTRESS_GC_BATCH_MAGIC 0x31434746	// 'FGC1'

class CFrontressGCConnection : public GCSDK::CProtoBufMsgBase::IProtoBufSendHandler
{
public:
	CFrontressGCConnection();
	~CFrontressGCConnection();

	// Is a coordinator configured at all? When it is not, everything here is
	// inert and the game behaves exactly as it did before.
	bool BEnabled() const;

	// Have we heard back from it recently enough to call ourselves connected?
	bool BConnected() const { return m_bConnected; }

	// Queue a message for the coordinator. Both the raw form (which is what
	// CGCClientSystem::BSendMessage hands us for struct messages) and the
	// IProtoBufSendHandler form CProtoBufMsgBase::BAsyncSend calls.
	bool BSendRawMessage( uint32 unMsgType, const uint8 *pubData, uint32 cubData );
	virtual bool BAsyncSend( GCSDK::MsgType_t eMsg, const uint8 *pubMsgBytes, uint32 cubSize ) OVERRIDE;

	// Pumped once a frame from CGCClientSystem. Sends what is queued, polls for
	// what is waiting, and dispatches whatever came back into the job manager.
	void Update();

	void Shutdown();

	// Drop the session: the next Update() says hello again from scratch.
	void Reset();

private:
	friend class CFrontressGCRequest;

	// Called by CFrontressGCRequest::OnCompleted once the HTTP exchange comes
	// back (or fails outright). Clears m_pPending and updates the poll/backoff
	// state. Defined in the .cpp; was missing here, which is a compile error
	// since it is called from Flush() and defined out-of-line.
	void OnRequestFinished( CFrontressGCRequest *pRequest, bool bOK );

	ISteamHTTP *GetHTTP() const;
	CSteamID GetLocalSteamID() const;
	bool BIsGameServer() const;

	void Flush();
	void OnBatchReceived( const uint8 *pubData, uint32 cubData );
	void DispatchMessage( const uint8 *pubMsg, uint32 cubMsg );
	void SetConnected( bool bConnected );
	bool BSetAuthHeaders( HTTPRequestHandle hRequest );

	CUtlVector< CUtlBuffer * > m_vecOutbound;
	class CFrontressGCRequest *m_pPending;

	bool   m_bConnected;
	double m_flNextPoll;
	double m_flBackoff;

	// The client's auth ticket is acquired once and reused: asking Steam for a
	// new one per request leaks handles and tells the coordinator nothing new.
	CUtlString m_sAuthTicket;
	HAuthTicket m_hAuthTicket;
};

CFrontressGCConnection &FrontressGC();

#endif // FRONTRESS_GC_H
