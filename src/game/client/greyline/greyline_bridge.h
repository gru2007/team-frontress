//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: the loopback link between the game's main menu web panel and the
// engine — see game/tc2/loose/resource/html/greyline.html's own "Bridge"
// object for the wire protocol this implements. Nothing here knows anything
// about the war or the coordinator: it is three primitives (run a console
// command, read a convar, make an HTTP request) that the menu page composes
// into the whole GREYLINE client, including — once it is offered host —
// standing up a P2P battle server. See the coordinator README's "when
// nothing dedicated is free" section for that flow.
//
//=========================================================================//

#ifndef GREYLINE_BRIDGE_H
#define GREYLINE_BRIDGE_H
#ifdef _WIN32
#pragma once
#endif

#include "igamesystem.h"
#include "GameEventListener.h"
#include "utlbuffer.h"
#include "greyline_http.h"

//-----------------------------------------------------------------------------
// Purpose: a loopback-only websocket JSON-RPC server for the menu web panel.
//
// One connection at a time: there is exactly one legitimate client (this
// game's own CEF panel), so a new connection simply replaces whatever was
// there, which is what a reloaded or reopened menu page looks like.
//
// This is a hand-rolled RFC 6455 server rather than a library, for the same
// reason greyline_gc.cpp hand-rolled its TCP link: the whole job is a few
// hundred lines against a fixed, versioned spec, on a socket nothing outside
// this machine can ever reach, and that is a smaller thing to get right and
// keep right than a general-purpose dependency this tree does not otherwise
// carry a build recipe for.
//-----------------------------------------------------------------------------
class CGreylineBridge : public CAutoGameSystemPerFrame, public CGameEventListener
{
public:
	CGreylineBridge();

	virtual bool Init() OVERRIDE;
	virtual void Shutdown() OVERRIDE;
	virtual void Update( float flFrameTime ) OVERRIDE;
	virtual void FireGameEvent( IGameEvent *pEvent ) OVERRIDE;

	// Sends one JSON-RPC reply frame: {"i":nID,"r":"<escaped pszResult>"}.
	// Public because the async fetch callback (a free function, so it can be
	// handed to greyline::CGreylineHTTP::Send as a plain function pointer)
	// needs to complete a reply well after DispatchRPCMessage returned.
	void Reply( int nID, const char *pszResult );

private:
	enum ConnState_t
	{
		CONN_HANDSHAKE = 0,
		CONN_OPEN,
	};

	struct Conn_t
	{
		int			nSocket;
		ConnState_t	eState;
		CUtlBuffer	RecvBuf;
		CUtlBuffer	SendBuf;
	};

	void OpenListener();
	void CloseListener();
	void AcceptNew();
	void CloseConn( const char *pszReason );
	void PumpRecv();
	void PumpSend();

	bool TryCompleteHandshake();
	void ProcessFrames();
	bool ExtractOneFrame( CUtlBuffer &buf, int &opcode, CUtlBuffer &payloadOut );
	void SendFrame( int opcode, const void *pData, int nLen );

	void DispatchRPCMessage( int nID, const char *pszMethod, const char *pszParams );

	void HandleFetch( int nID, const char *pszParamsJSON );

	int		m_nListenSocket;
	Conn_t *m_pConn;

	int		m_nGameOverSeq;
};

CGreylineBridge *GreylineBridge();

#endif // GREYLINE_BRIDGE_H
