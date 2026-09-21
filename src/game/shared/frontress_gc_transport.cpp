#include "cbase.h"
#include "frontress_gc_transport.h"

#include <stdlib.h>

#include "fmtstr.h"
#include "gcsdk/webapi_response.h"
#ifdef CLIENT_DLL
#include "clientsteamcontext.h"
#else
#include "enginecallback.h"
#include "steam/steam_api.h"
#endif

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

ConVar tf_gc_address( "tf_gc_address", "https://gc.team-frontress.org", FCVAR_ARCHIVE,
	"Base URL of the Frontress game coordinator." );
#ifndef CLIENT_DLL
ConVar tf_gc_server_token( "tf_gc_server_token", "", FCVAR_HIDDEN | FCVAR_PROTECTED,
	"Credential used by a dedicated server to open its GC session." );
ConVar tf_gc_match_id( "tf_gc_match_id", "", FCVAR_HIDDEN | FCVAR_PROTECTED,
	"Coordinator match owned by this dedicated server." );
#endif

static const uint32 k_unMaxGCResponse = 1024 * 1024;
static const uint32 k_unMaxGCPacket = 256 * 1024;
static const uint32 k_unMaxGCQueuedBytes = 768 * 1024;
static const int k_nMaxGCMessages = 256;
static const char *k_pszGCIdentity = "frontress-coordinator";
static const char *k_pszInventoryIdentity = "tf2sdk";

static const char *GCAddress()
{
#ifndef CLIENT_DLL
	const char *value = getenv( "GC_URL" );
	if ( value && value[0] ) return value;
#endif
	return tf_gc_address.GetString();
}

#ifndef CLIENT_DLL
static const char *GCServerToken()
{
	const char *value = getenv( "GC_SECRET" );
	return value && value[0] ? value : tf_gc_server_token.GetString();
}

static const char *GCMatchID()
{
	const char *value = getenv( "MATCH_ID" );
	return value && value[0] ? value : tf_gc_match_id.GetString();
}
#endif

CFrontressGameCoordinator::CFrontressGameCoordinator()
	: m_hRequest( INVALID_HTTPREQUEST_HANDLE )
	, m_flNextExchange( 0.0 )
	, m_unClientSequence( 1 )
	, m_unLastServerSequence( 0 )
	, m_nConsecutiveFailures( 0 )
	, m_bConnected( false )
#ifdef CLIENT_DLL
	, m_hAuthTicket( k_HAuthTicketInvalid )
	, m_hInventoryTicket( k_HAuthTicketInvalid )
#endif
{
	GenerateInstanceID();
}

CFrontressGameCoordinator::~CFrontressGameCoordinator()
{
	Shutdown();
}

ISteamHTTP *CFrontressGameCoordinator::GetHTTP() const
{
#ifdef CLIENT_DLL
	return SteamHTTP();
#else
	return SteamGameServerHTTP();
#endif
}

EGCResults CFrontressGameCoordinator::SendMessage( uint32 unMsgType, const void *pubData, uint32 cubData )
{
	if ( !pubData || cubData < 8 || cubData > k_unMaxGCPacket )
		return k_EGCResultInvalidMessage;
	if ( m_Outbox.Count() + m_InFlight.Count() >= k_nMaxGCMessages )
		return k_EGCResultInvalidMessage;
	uint32 unQueuedBytes = cubData;
	FOR_EACH_VEC( m_Outbox, i ) unQueuedBytes += m_Outbox[i].m_Data.Count();
	FOR_EACH_VEC( m_InFlight, i ) unQueuedBytes += m_InFlight[i].m_Data.Count();
	if ( unQueuedBytes > k_unMaxGCQueuedBytes )
		return k_EGCResultInvalidMessage;
	Message_t msg;
	msg.m_unType = unMsgType & 0x7fffffff;
	msg.m_Data.SetCount( cubData );
	Q_memcpy( msg.m_Data.Base(), pubData, cubData );
	m_Outbox.AddToTail( msg );
	return k_EGCResultOK;
}

bool CFrontressGameCoordinator::IsMessageAvailable( uint32 *pcubMsgSize )
{
	if ( m_Inbox.Count() == 0 )
		return false;
	if ( pcubMsgSize )
		*pcubMsgSize = m_Inbox.Head().m_Data.Count();
	return true;
}

EGCResults CFrontressGameCoordinator::RetrieveMessage( uint32 *punMsgType, void *pubDest, uint32 cubDest, uint32 *pcubMsgSize )
{
	if ( m_Inbox.Count() == 0 )
	{
		if ( pcubMsgSize ) *pcubMsgSize = 0;
		return k_EGCResultNoMessage;
	}
	const Message_t &msg = m_Inbox.Head();
	if ( pcubMsgSize ) *pcubMsgSize = msg.m_Data.Count();
	if ( cubDest < (uint32)msg.m_Data.Count() )
		return k_EGCResultBufferTooSmall;
	if ( punMsgType ) *punMsgType = msg.m_unType | 0x80000000;
	Q_memcpy( pubDest, msg.m_Data.Base(), msg.m_Data.Count() );
	m_Inbox.Remove( 0 );
	return k_EGCResultOK;
}

void CFrontressGameCoordinator::Pump()
{
	if ( m_hRequest != INVALID_HTTPREQUEST_HANDLE || Plat_FloatTime() < m_flNextExchange )
		return;
	if ( BCanExchange() )
		StartExchange();
}

bool CFrontressGameCoordinator::BCanExchange()
{
	if ( !GetHTTP() || GCAddress()[0] == '\0' )
		return false;
#ifdef CLIENT_DLL
	if ( !steamapicontext || !steamapicontext->SteamUser() || !steamapicontext->SteamUser()->BLoggedOn() )
		return false;
	if ( m_strTicket.IsEmpty() || m_strInventoryTicket.IsEmpty() )
	{
		RequestAuthTicket();
		return false;
	}
#else
	if ( GCServerToken()[0] == '\0' || GCMatchID()[0] == '\0' )
		return false;
#endif
	return true;
}

#ifdef CLIENT_DLL
void CFrontressGameCoordinator::RequestAuthTicket()
{
	if ( m_hAuthTicket == k_HAuthTicketInvalid && steamapicontext && steamapicontext->SteamUser() )
		m_hAuthTicket = steamapicontext->SteamUser()->GetAuthTicketForWebApi( k_pszGCIdentity );
	if ( m_hInventoryTicket == k_HAuthTicketInvalid && steamapicontext && steamapicontext->SteamUser() )
		m_hInventoryTicket = steamapicontext->SteamUser()->GetAuthTicketForWebApi( k_pszInventoryIdentity );
}

void CFrontressGameCoordinator::OnWebApiTicket( GetTicketForWebApiResponse_t *pResponse )
{
	if ( !pResponse || ( pResponse->m_hAuthTicket != m_hAuthTicket && pResponse->m_hAuthTicket != m_hInventoryTicket ) )
		return;
	if ( pResponse->m_eResult != k_EResultOK )
	{
		if ( pResponse->m_hAuthTicket == m_hAuthTicket ) m_hAuthTicket = k_HAuthTicketInvalid;
		if ( pResponse->m_hAuthTicket == m_hInventoryTicket ) m_hInventoryTicket = k_HAuthTicketInvalid;
		m_flNextExchange = Plat_FloatTime() + 5.0;
		return;
	}
	CUtlVector< char > hex;
	hex.SetCount( pResponse->m_cubTicket * 2 + 1 );
	for ( int i = 0; i < pResponse->m_cubTicket; ++i )
		V_snprintf( &hex[i * 2], 3, "%02x", pResponse->m_rgubTicket[i] );
	hex[pResponse->m_cubTicket * 2] = '\0';
	if ( pResponse->m_hAuthTicket == m_hAuthTicket )
		m_strTicket = hex.Base();
	else
		m_strInventoryTicket = hex.Base();
}
#endif

void CFrontressGameCoordinator::StartExchange()
{
	if ( m_InFlight.Count() == 0 && m_Outbox.Count() != 0 )
		m_InFlight.Swap( m_Outbox );

	GCSDK::CWebAPIResponse response;
	response.SetJSONAnonymousRootNode( true );
	GCSDK::CWebAPIValues *root = response.CreateRootValue( "body" );
	root->SetChildInt32Value( "protocol", 2 );
	root->SetChildStringValue( "session_id", m_strSessionID.Get() );
	root->SetChildStringValue( "instance_id", m_strInstanceID.Get() );
	root->SetChildUInt64Value( "client_sequence", m_unClientSequence );
	root->SetChildUInt64Value( "ack_server_sequence", m_unLastServerSequence );
#ifdef CLIENT_DLL
	root->SetChildStringValue( "role", "client" );
	root->SetChildStringValue( "steam_id", CFmtStr( "%llu", steamapicontext->SteamUser()->GetSteamID().ConvertToUint64() ) );
	root->SetChildStringValue( "ticket", m_strTicket.Get() );
	root->SetChildStringValue( "inventory_ticket", m_strInventoryTicket.Get() );
	if ( steamapicontext->SteamUtils() )
		root->SetChildUInt32Value( "app_id", steamapicontext->SteamUtils()->GetAppID() );
#else
	root->SetChildStringValue( "role", "server" );
	if ( steamgameserverapicontext && steamgameserverapicontext->SteamGameServer() )
		root->SetChildStringValue( "steam_id", CFmtStr( "%llu", steamgameserverapicontext->SteamGameServer()->GetSteamID().ConvertToUint64() ) );
	root->SetChildStringValue( "server_token", GCServerToken() );
	root->SetChildStringValue( "match_id", GCMatchID() );
#endif
	GCSDK::CWebAPIValues *messages = root->CreateChildArray( "messages", "message" );
	FOR_EACH_VEC( m_InFlight, i )
	{
		GCSDK::CWebAPIValues *item = messages->AddChildObjectToArray();
		item->SetChildUInt32Value( "type", m_InFlight[i].m_unType );
		CUtlMemory< char > encoded;
		if ( GCSDK::Base64EncodeIntoUTLMemory( m_InFlight[i].m_Data.Base(), m_InFlight[i].m_Data.Count(), encoded ) )
			item->SetChildStringValue( "data", encoded.Base() );
	}

	CUtlBuffer body( 0, 0, CUtlBuffer::TEXT_BUFFER );
	if ( !response.BEmitFormattedOutput( GCSDK::k_EWebAPIOutputFormat_JSON, body, k_unMaxGCResponse ) )
	{
		ScheduleRetry();
		return;
	}
	CFmtStrMax url( "%s/v1/gc/exchange", GCAddress() );
	ISteamHTTP *http = GetHTTP();
	HTTPRequestHandle request = http->CreateHTTPRequest( k_EHTTPMethodPOST, url.Get() );
	if ( request == INVALID_HTTPREQUEST_HANDLE ) { ScheduleRetry(); return; }
	http->SetHTTPRequestNetworkActivityTimeout( request, 15 );
	http->SetHTTPRequestRawPostBody( request, "application/json", (uint8 *)body.Base(), body.TellMaxPut() );
	SteamAPICall_t call = k_uAPICallInvalid;
	if ( !http->SendHTTPRequest( request, &call ) ) { http->ReleaseHTTPRequest( request ); ScheduleRetry(); return; }
	m_hRequest = request;
	m_callCompleted.Set( call, this, &CFrontressGameCoordinator::OnExchangeCompleted );
}

void CFrontressGameCoordinator::OnExchangeCompleted( HTTPRequestCompleted_t *pInfo, bool bIOFailure )
{
	ISteamHTTP *http = GetHTTP();
	HTTPRequestHandle request = m_hRequest;
	m_hRequest = INVALID_HTTPREQUEST_HANDLE;
	if ( !http || bIOFailure || !pInfo || !pInfo->m_bRequestSuccessful || pInfo->m_eStatusCode != k_EHTTPStatusCode200OK )
	{
		if ( pInfo && ( pInfo->m_eStatusCode == k_EHTTPStatusCode401Unauthorized ||
			pInfo->m_eStatusCode == k_EHTTPStatusCode403Forbidden ||
			pInfo->m_eStatusCode == k_EHTTPStatusCode409Conflict ) )
		{
			ResetSession( true );
#ifdef CLIENT_DLL
			if ( pInfo->m_eStatusCode == k_EHTTPStatusCode401Unauthorized || pInfo->m_eStatusCode == k_EHTTPStatusCode403Forbidden )
			{
				if ( m_hAuthTicket != k_HAuthTicketInvalid && steamapicontext && steamapicontext->SteamUser() )
					steamapicontext->SteamUser()->CancelAuthTicket( m_hAuthTicket );
				m_hAuthTicket = k_HAuthTicketInvalid;
				m_strTicket.Clear();
			}
#endif
		}
		ScheduleRetry();
		if ( http && request != INVALID_HTTPREQUEST_HANDLE ) http->ReleaseHTTPRequest( request );
		return;
	}
	bool bParsed = false;
	uint32 bytes = 0;
	if ( http->GetHTTPResponseBodySize( request, &bytes ) && bytes > 0 && bytes <= k_unMaxGCResponse )
	{
		CUtlVector< uint8 > data; data.SetCount( bytes + 1 ); data[bytes] = 0;
		if ( http->GetHTTPResponseBodyData( request, data.Base(), bytes ) ) bParsed = ParseResponse( data.Base(), bytes );
	}
	http->ReleaseHTTPRequest( request );
	if ( !bParsed )
	{
		ScheduleRetry();
		return;
	}
	m_InFlight.RemoveAll();
	++m_unClientSequence;
	m_nConsecutiveFailures = 0;
}

bool CFrontressGameCoordinator::ParseResponse( const void *pData, uint32 cubData )
{
	CUtlBuffer json( pData, cubData, CUtlBuffer::READ_ONLY | CUtlBuffer::TEXT_BUFFER );
	GCSDK::CWebAPIValues *root = GCSDK::CWebAPIValues::ParseJSON( json );
	if ( !root ) return false;
	CUtlString strSessionID;
	root->GetChildStringValue( strSessionID, "session_id", "" );
	const uint64 unClientSequence = root->GetChildUInt64Value( "client_sequence", 0 );
	const uint64 unServerSequence = root->GetChildUInt64Value( "server_sequence", 0 );
	if ( strSessionID.IsEmpty() || ( !m_strSessionID.IsEmpty() && Q_strcmp( strSessionID.Get(), m_strSessionID.Get() ) != 0 ) ||
		unClientSequence != m_unClientSequence || unServerSequence < m_unLastServerSequence || unServerSequence > m_unLastServerSequence + 1 )
	{
		delete root;
		return false;
	}

	CUtlVector< Message_t > decodedMessages;
	GCSDK::CWebAPIValues *messages = root->FindChild( "messages" );
	for ( GCSDK::CWebAPIValues *item = messages ? messages->GetFirstChild() : NULL; item; item = item->GetNextChild() )
	{
		if ( decodedMessages.Count() >= k_nMaxGCMessages ) { delete root; return false; }
		CUtlString encoded; item->GetChildStringValue( encoded, "data", "" );
		if ( encoded.IsEmpty() || encoded.Length() > (int)( ( k_unMaxGCPacket * 4 ) / 3 + 8 ) ) { delete root; return false; }
		Message_t msg; msg.m_unType = item->GetChildUInt32Value( "type", 0 );
		msg.m_Data.SetCount( encoded.Length() );
		uint32 decoded = msg.m_Data.Count();
		if ( !GCSDK::Base64Decode( encoded.Get(), encoded.Length(), msg.m_Data.Base(), &decoded, false ) || decoded < 8 || decoded > k_unMaxGCPacket )
			{ delete root; return false; }
		msg.m_Data.SetCount( decoded );
		uint32 unWireType = 0;
		Q_memcpy( &unWireType, msg.m_Data.Base(), sizeof( unWireType ) );
		unWireType = LittleDWord( unWireType );
		if ( ( unWireType & 0x80000000 ) == 0 || ( unWireType & 0x7fffffff ) != msg.m_unType )
			{ delete root; return false; }
		decodedMessages.AddToTail( msg );
	}
	if ( m_Inbox.Count() + decodedMessages.Count() > k_nMaxGCMessages )
		{ delete root; return false; }
	if ( decodedMessages.Count() != 0 && unServerSequence == m_unLastServerSequence )
		{ delete root; return false; }

	m_strSessionID = strSessionID;
	m_bConnected = root->GetChildBoolValue( "connected", false );
	m_unLastServerSequence = unServerSequence;
	FOR_EACH_VEC( decodedMessages, i ) m_Inbox.AddToTail( decodedMessages[i] );
	int nPollAfter = root->GetChildInt32Value( "poll_after_ms", 250 );
	if ( nPollAfter < 100 ) nPollAfter = 100;
	if ( nPollAfter > 5000 ) nPollAfter = 5000;
	m_flNextExchange = Plat_FloatTime() + (double)nPollAfter / 1000.0;
	delete root;
	return true;
}

void CFrontressGameCoordinator::GenerateInstanceID()
{
	uint8 bytes[16];
	SecureRandomBytes( bytes, sizeof( bytes ) );
	static const char hex[] = "0123456789abcdef";
	char id[33];
	for ( int i = 0; i < 16; ++i )
	{
		id[i * 2] = hex[bytes[i] >> 4];
		id[i * 2 + 1] = hex[bytes[i] & 0x0f];
	}
	id[32] = '\0';
	m_strInstanceID = id;
}

void CFrontressGameCoordinator::ScheduleRetry()
{
	if ( m_nConsecutiveFailures < 5 ) ++m_nConsecutiveFailures;
	const int nSeconds = 1 << ( m_nConsecutiveFailures - 1 );
	m_flNextExchange = Plat_FloatTime() + nSeconds;
}

void CFrontressGameCoordinator::ResetSession( bool bNewInstance )
{
	m_strSessionID.Clear();
	m_bConnected = false;
	m_unClientSequence = 1;
	m_unLastServerSequence = 0;
	if ( bNewInstance ) GenerateInstanceID();
	// Queued, in-flight and already delivered packets survive a reconnect. The
	// Valve GC client owns their reliable-job lifetime; this layer only transports them.
}

void CFrontressGameCoordinator::Shutdown()
{
	if ( m_hRequest != INVALID_HTTPREQUEST_HANDLE && GetHTTP() ) GetHTTP()->ReleaseHTTPRequest( m_hRequest );
	m_callCompleted.Cancel(); m_hRequest = INVALID_HTTPREQUEST_HANDLE;
#ifdef CLIENT_DLL
	if ( m_hAuthTicket != k_HAuthTicketInvalid && steamapicontext && steamapicontext->SteamUser() )
		steamapicontext->SteamUser()->CancelAuthTicket( m_hAuthTicket );
	if ( m_hInventoryTicket != k_HAuthTicketInvalid && steamapicontext && steamapicontext->SteamUser() )
		steamapicontext->SteamUser()->CancelAuthTicket( m_hInventoryTicket );
	m_hAuthTicket = k_HAuthTicketInvalid; m_strTicket.Clear();
	m_hInventoryTicket = k_HAuthTicketInvalid; m_strInventoryTicket.Clear();
#endif
	ResetSession( false );
	m_Outbox.RemoveAll();
	m_InFlight.RemoveAll();
	m_Inbox.RemoveAll();
}
