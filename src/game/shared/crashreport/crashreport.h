//========= Copyright Valve Corporation, All rights reserved. ============//
//
// Purpose: Crash reporter. See crashreport.cpp.
//
//=============================================================================

#ifndef CRASHREPORT_H
#define CRASHREPORT_H

#ifdef _WIN32
#pragma once
#endif

#include <string>

#include "igamesystem.h"

class CCrashReporter : public CAutoGameSystemPerFrame
{
public:
	CCrashReporter();

	virtual bool Init();
	virtual void Shutdown();
#ifdef CLIENT_DLL
	virtual void Update( float frametime );
#else
	virtual void FrameUpdatePostEntityThink();
#endif

	static void AssertCallbackFunc( const char *pchFile, int nLine, const char *pchMessage );
	static SpewRetval_t SpewFunc( SpewType_t type, const tchar* pMsg );

	void ReportInfo( const char *logger, const char* message );
	void ReportWarning( const char* logger, const char* message );
	void ReportError( const char* logger, const char* message );

private:
	void SendPendingReports();
	void Tick();

	bool m_bInit = false;
	bool m_bInProcess = false;	// the game writes its own dumps (Windows)
	bool m_bUserSet = false;
	double m_flNextSave = 0.0;
	std::string m_ReportsDir;	// pending dumps and console output
	std::string m_SessionPath;	// this session's console output
	std::string m_DatabasePath;	// sentry's and crashpad's own files
	SpewOutputFunc_t m_OutputFuncPrev = NULL;
};

extern CCrashReporter* GetCrashReporter();
#endif
