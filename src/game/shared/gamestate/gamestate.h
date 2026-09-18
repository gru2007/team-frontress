//========= Copyright Valve Corporation, All rights reserved. ============//
//
// Purpose: 
//
//=============================================================================

#ifndef GAMESTATE_H
#define GAMESTATE_H

#ifdef _WIN32
#pragma once
#endif

#include <utility>
#include <functional>
#include <string>
#include <vector>
#include "igamesystem.h"

class CGameStateManager : public CAutoGameSystemPerFrame, public CGameEventListener
{
public:
	CGameStateManager();

	virtual bool Init() OVERRIDE;
	virtual void Update(float frametime) OVERRIDE;
	virtual void Shutdown() OVERRIDE;
	virtual char const* Name() OVERRIDE { return "CGameStateManager"; }
	virtual void FireGameEvent( IGameEvent* event ) OVERRIDE;
	void RegisterMethod(std::string methodName, const std::function<std::pair<bool, std::string>(const std::string& params, int64_t iRpcId) >& method);
	void UnregisterMethod(std::string methodName);
	void QueueReturn(int64_t iRpcId, const std::string& strValue);
	void QueueEvent(const std::string& strEvent, const std::string& strParams);

	// The campaign map page (resource/html/campaign.html) is drawn by the same
	// local server that serves the HTML menu, and reads the war from
	// GET /v1/campaign. The game publishes that document here -- the HTTP
	// thread only ever hands out the last copy it was given, so nothing on the
	// page can block the frame.
	void SetCampaignJSON( const std::string &strJSON );

	// What the page has POSTed to /v1/campaign/command since the last call,
	// oldest first, and cleared by the call. One line per command: a verb and
	// its argument, e.g. "deploy works". Drained on the main thread.
	void TakeCampaignCommands( std::vector< std::string > &vecOut );

	bool IsReady() { return m_bReady; }
	void MarkReady() { m_bReady = true; }
	bool IsUIReady() { return m_bUIReady; }
	void MarkUIReady() { m_bUIReady = true; }
	
	void InitSubscriptions();
	void StopSubscriptions();

private:
	bool m_bInit = false;
	bool m_bReady = false;
	bool m_bUIReady = false;
	bool m_bListeningToEvents = false;
	class CHTTPServerThread* m_pServerThread = NULL;
};

extern CGameStateManager* GetGameStateManager();
#endif
