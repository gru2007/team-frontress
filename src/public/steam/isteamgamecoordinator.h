//====== Copyright Valve Corporation, All rights reserved. =======//
#ifndef ISTEAMGAMECOORDINATOR
#define ISTEAMGAMECOORDINATOR
#ifdef _WIN32
#pragma once
#endif

#include "steam_api_common.h"

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
	virtual EGCResults SendMessage( uint32 unMsgType, const void *pubData, uint32 cubData ) = 0;
	virtual bool IsMessageAvailable( uint32 *pcubMsgSize ) = 0;
	virtual EGCResults RetrieveMessage( uint32 *punMsgType, void *pubDest, uint32 cubDest, uint32 *pcubMsgSize ) = 0;
};

#define STEAMGAMECOORDINATOR_INTERFACE_VERSION "SteamGameCoordinator001"
#endif
