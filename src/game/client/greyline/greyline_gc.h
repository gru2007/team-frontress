//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: the client's link to the Greyline Game Coordinator.
//
// Framed protobuf over TCP. The socket is non-blocking and polled from the
// main thread: the link carries a handful of messages per match plus a
// heartbeat, so a worker thread would buy nothing and cost every race it could
// have with the lobby module, which also lives on the main thread.
//
//=========================================================================//

#ifndef GREYLINE_GC_H
#define GREYLINE_GC_H
#ifdef _WIN32
#pragma once
#endif

#include "igamesystem.h"
#include "GameEventListener.h"
#include "utlbuffer.h"
#include "steam/steam_api.h"

namespace greyline
{
	class CMsgEnvelope;
}

enum GreylineGCState_t
{
	GC_OFFLINE = 0,		// no socket; either disabled or waiting to retry
	GC_CONNECTING,		// non-blocking connect in flight
	GC_HANDSHAKE,		// connected, hello sent, waiting for the welcome
	GC_READY,			// authenticated
};

//-----------------------------------------------------------------------------
// Purpose: talks to the coordinator and drives the lobby module from what it
//			says. Nothing here touches game traffic.
//-----------------------------------------------------------------------------
class CGreylineGC : public CAutoGameSystemPerFrame, public CGameEventListener
{
public:
	CGreylineGC();

	virtual bool Init() OVERRIDE;
	virtual void Shutdown() OVERRIDE;
	virtual void Update( float frametime ) OVERRIDE;
	virtual void FireGameEvent( IGameEvent *pEvent ) OVERRIDE;

	// --- driven by the player / UI ---
	void Connect();
	void Disconnect( const char *pszReason );
	void RequestDeploy( int eSide, const char *pszFrontID );
	void CancelDeploy();
	void RequestWorldState();

	// --- driven by the lobby module ---
	void OnBattleHosted( uint64 ulLobbyID, uint64 ulGameServerID, const char *pszMap );
	void OnHostFailed( const char *pszReason );
	void OnJoinedBattle( bool bConnected, uint32 unRTTMs, const char *pszFailure );

	GreylineGCState_t	GetState() const	{ return m_eState; }
	const char *		GetStateName() const;
	const char *		GetQueueMessage() const { return m_szQueueMessage; }
	bool				IsReady() const		{ return m_eState == GC_READY; }
	const char *		GetMatchID() const	{ return m_szMatchID; }
	bool				IsHost() const		{ return m_bIsHost; }

private:
	void SetState( GreylineGCState_t eState );
	void CloseSocket();
	bool PumpConnect();
	void PumpRecv();
	void PumpSend();
	void PumpHeartbeat();
	void DispatchFrame( const byte *pData, int nLength );
	void Send( const greyline::CMsgEnvelope &env );
	void SendHello();

	void HandleWelcome( const greyline::CMsgEnvelope &env );
	void HandleAssignHost( const greyline::CMsgEnvelope &env );
	void HandleJoinBattle( const greyline::CMsgEnvelope &env );
	void HandleMigrateHost( const greyline::CMsgEnvelope &env );
	void HandleQueueStatus( const greyline::CMsgEnvelope &env );
	void HandleMatchOver( const greyline::CMsgEnvelope &env );
	void HandleSecurityVerdict( const greyline::CMsgEnvelope &env );

	void ReportMatchResult();
	void FillCapabilities( greyline::CMsgEnvelope &env );

	GreylineGCState_t	m_eState;
	int					m_nSocket;			// -1 when closed
	float				m_flRetryAt;
	float				m_flRetryDelay;
	float				m_flNextHeartbeat;
	float				m_flHeartbeatInterval;
	uint64				m_ulJobID;

	CUtlBuffer			m_RecvBuffer;
	CUtlBuffer			m_SendBuffer;

	char				m_szMatchID[64];
	bool				m_bIsHost;
	bool				m_bResultReported;
	char				m_szQueueMessage[128];

	// Auth ticket, fetched once per session and reused across reconnects.
	byte				m_TicketBuffer[1024];
	uint32				m_cbTicket;
	HAuthTicket			m_hAuthTicket;
};

CGreylineGC *GreylineGC();

#endif // GREYLINE_GC_H
