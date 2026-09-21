//========= Copyright Team Frontress, All rights reserved. ============//
// A Steam Game Coordinator transport backed by the Frontress gateway.
// Valve's CGCClient remains completely unaware of the substitution.
//=====================================================================//
#ifndef FRONTRESS_GC_TRANSPORT_H
#define FRONTRESS_GC_TRANSPORT_H
#ifdef _WIN32
#pragma once
#endif

#include "steam/steam_api_common.h"
#include "steam/isteamgamecoordinator.h"
#include "tier1/utlstring.h"
#include "tier1/utlvector.h"

#ifdef CLIENT_DLL
#include "steam/isteamuser.h"
#endif

class CFrontressGameCoordinator : public ISteamGameCoordinator
{
public:
	CFrontressGameCoordinator();
	virtual ~CFrontressGameCoordinator();

	virtual EGCResults SendMessage( uint32 unMsgType, const void *pubData, uint32 cubData ) OVERRIDE;
	virtual bool IsMessageAvailable( uint32 *pcubMsgSize ) OVERRIDE;
	virtual EGCResults RetrieveMessage( uint32 *punMsgType, void *pubDest, uint32 cubDest, uint32 *pcubMsgSize ) OVERRIDE;

	void Pump();
	void Shutdown();
	bool BConnected() const { return m_bConnected; }

private:
	struct Message_t
	{
		Message_t() : m_unType( 0 ) {}
		Message_t( const Message_t &other ) : m_unType( other.m_unType )
		{
			m_Data.CopyArray( other.m_Data.Base(), other.m_Data.Count() );
		}
		Message_t &operator=( const Message_t &other )
		{
			if ( this != &other )
			{
				m_unType = other.m_unType;
				m_Data.CopyArray( other.m_Data.Base(), other.m_Data.Count() );
			}
			return *this;
		}

		uint32 m_unType;
		CUtlVector< uint8 > m_Data;
	};

	ISteamHTTP *GetHTTP() const;
	bool BCanExchange();
	void StartExchange();
	void OnExchangeCompleted( HTTPRequestCompleted_t *pInfo, bool bIOFailure );
	bool ParseResponse( const void *pData, uint32 cubData );
	void ResetSession( bool bNewInstance );
	void GenerateInstanceID();
	void ScheduleRetry();

#ifdef CLIENT_DLL
	void RequestAuthTicket();
	STEAM_CALLBACK( CFrontressGameCoordinator, OnWebApiTicket, GetTicketForWebApiResponse_t );
#endif

	CUtlString m_strSessionID;
	CUtlString m_strInstanceID;
	CUtlString m_strTicket;
	CUtlString m_strInventoryTicket;
	CUtlVector< Message_t > m_Outbox;
	CUtlVector< Message_t > m_InFlight;
	CUtlVector< Message_t > m_Inbox;
	HTTPRequestHandle m_hRequest;
	CCallResult< CFrontressGameCoordinator, HTTPRequestCompleted_t > m_callCompleted;
	double m_flNextExchange;
	uint64 m_unClientSequence;
	uint64 m_unLastServerSequence;
	int m_nConsecutiveFailures;
	bool m_bConnected;
#ifdef CLIENT_DLL
	HAuthTicket m_hAuthTicket;
	HAuthTicket m_hInventoryTicket;
#endif
};

#endif
