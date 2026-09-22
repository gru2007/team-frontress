//========= Copyright Team Frontress, All rights reserved. ===================//
//
// Purpose: See frontress_gc.h. The transport that replaces Valve's GC.
//
//=============================================================================//

#include "cbase.h"
#include "frontress_gc.h"
#include "gc_clientsystem.h"

#include "gcsdk/gcclient.h"
#include "gcsdk/gcconstants.h"
#include "gcsdk/jobmgr.h"
#include "gcsdk/msgbase.h"
#include "gcsdk/netpacket.h"
#include "gcsdk/netpacketpool.h"

#ifdef CLIENT_DLL
#include "clientsteamcontext.h"
#include "steam/isteamuser.h"
#else
#include "steam/steam_api.h"
#include "enginecallback.h"
#endif

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

using namespace GCSDK;

//-----------------------------------------------------------------------------
// Where the coordinator lives. Empty is the honest default: without it nothing
// here runs and the game is what it was.
//-----------------------------------------------------------------------------
ConVar tf_gc_address( "tf_gc_address", "https://gc.team-frontress.org", FCVAR_ARCHIVE
#ifdef GAME_DLL
                      | FCVAR_GAMEDLL
#endif
                      , "Base URL of the Team Frontress coordinator, e.g. http://gc.example.org:27100. Empty disables it." );

ConVar tf_gc_debug( "tf_gc_debug", "0", FCVAR_NONE, "Log every GC message that crosses the transport." );

#ifdef GAME_DLL
// A dedicated server has no Steam user to sign as, so it signs with the shared
// secret instead. This is the same convar the WebAPI path already uses.
extern ConVar sv_private_token;
#endif

// A poll that found nothing waits this long before asking again. A poll that
// brought something back asks again immediately: the GC has more to say.
static const float k_flIdlePollInterval = 1.0f;
static const float k_flMaxBackoff       = 30.0f;
// How long the coordinator may hold a poll open before answering empty.
static const uint32 k_unLongPollSecs    = 20;

//-----------------------------------------------------------------------------
// One HTTP exchange. Owns itself: it is deleted when Steam calls it back, or
// when the connection is torn down under it.
//-----------------------------------------------------------------------------
class CFrontressGCRequest
{
public:
	CFrontressGCRequest( CFrontressGCConnection *pConnection, ISteamHTTP *pHTTP, HTTPRequestHandle hRequest )
		: m_pConnection( pConnection )
		, m_pHTTP( pHTTP )
		, m_hRequest( hRequest )
	{
	}

	void OnCompleted( HTTPRequestCompleted_t *pInfo, bool bIOFailure )
	{
		CFrontressGCConnection *pConnection = m_pConnection;
		m_pConnection = NULL;

		bool bOK = false;
		if ( pInfo && !bIOFailure && pInfo->m_bRequestSuccessful && pInfo->m_eStatusCode == k_EHTTPStatusCode200OK )
		{
			uint32 unBytes = 0;
			if ( m_pHTTP->GetHTTPResponseBodySize( m_hRequest, &unBytes ) )
			{
				CUtlBuffer bufResponse;
				if ( unBytes > 0 )
				{
					bufResponse.EnsureCapacity( unBytes );
					if ( m_pHTTP->GetHTTPResponseBodyData( m_hRequest, ( uint8 * )bufResponse.Base(), unBytes ) )
					{
						bufResponse.SeekPut( CUtlBuffer::SEEK_HEAD, unBytes );
					}
					else
					{
						unBytes = 0;
					}
				}

				bOK = true;
				if ( pConnection )
				{
					pConnection->OnBatchReceived( ( const uint8 * )bufResponse.Base(), unBytes );
				}
			}
		}
		else if ( pInfo && tf_gc_debug.GetBool() )
		{
			Warning( "[FrontressGC] poll failed, HTTP %d\n", pInfo->m_eStatusCode );
		}

		m_pHTTP->ReleaseHTTPRequest( m_hRequest );

		if ( pConnection )
		{
			pConnection->OnRequestFinished( this, bOK );
		}
		delete this;
	}

	// The connection is going away first. Let the callback land on nothing.
	void Orphan() { m_pConnection = NULL; }

	CCallResult< CFrontressGCRequest, HTTPRequestCompleted_t > m_CallResult;

private:
	CFrontressGCConnection *m_pConnection;
	ISteamHTTP             *m_pHTTP;
	HTTPRequestHandle       m_hRequest;
};

//-----------------------------------------------------------------------------
CFrontressGCConnection &FrontressGC()
{
	static CFrontressGCConnection s_Connection;
	return s_Connection;
}

//-----------------------------------------------------------------------------
CFrontressGCConnection::CFrontressGCConnection()
	: m_pPending( NULL )
	, m_bConnected( false )
	, m_flNextPoll( 0.0 )
	, m_flBackoff( 0.0 )
	, m_hAuthTicket( k_HAuthTicketInvalid )
{
}

CFrontressGCConnection::~CFrontressGCConnection()
{
	Shutdown();
}

//-----------------------------------------------------------------------------
bool CFrontressGCConnection::BEnabled() const
{
	const char *pszAddress = tf_gc_address.GetString();
	return pszAddress && pszAddress[0] != '\0';
}

//-----------------------------------------------------------------------------
bool CFrontressGCConnection::BIsGameServer() const
{
#ifdef GAME_DLL
	return engine->IsDedicatedServer();
#else
	return false;
#endif
}

ISteamHTTP *CFrontressGCConnection::GetHTTP() const
{
#ifdef GAME_DLL
	if ( engine->IsDedicatedServer() )
		return SteamGameServerHTTP();
#endif
	return SteamHTTP();
}

CSteamID CFrontressGCConnection::GetLocalSteamID() const
{
#ifdef GAME_DLL
	if ( engine->IsDedicatedServer() )
	{
		if ( steamgameserverapicontext && steamgameserverapicontext->SteamGameServer() )
			return steamgameserverapicontext->SteamGameServer()->GetSteamID();
		return CSteamID();
	}
#endif
	if ( steamapicontext && steamapicontext->SteamUser() )
		return steamapicontext->SteamUser()->GetSteamID();
	return CSteamID();
}

//-----------------------------------------------------------------------------
// Purpose: Queue an outbound message. Everything the game sends the GC ends up
//          here, whether it started as a struct message or a protobuf one.
//-----------------------------------------------------------------------------
bool CFrontressGCConnection::BSendRawMessage( uint32 unMsgType, const uint8 *pubData, uint32 cubData )
{
	if ( !BEnabled() || pubData == NULL || cubData == 0 )
		return false;

	// Never let a stalled coordinator grow the queue without bound. Dropping
	// the oldest is right: the newest message is the one that reflects what the
	// player just did.
	while ( m_vecOutbound.Count() >= 256 )
	{
		delete m_vecOutbound[ 0 ];
		m_vecOutbound.Remove( 0 );
	}

	CUtlBuffer *pBuf = new CUtlBuffer( 0, cubData, 0 );
	pBuf->Put( pubData, cubData );
	m_vecOutbound.AddToTail( pBuf );

	if ( tf_gc_debug.GetBool() )
	{
		Msg( "[FrontressGC] -> %s (%u bytes)\n", PchMsgNameFromEMsg( unMsgType & ~k_EMsgProtoBufFlag ), cubData );
	}

	// Something to say: stop waiting for the idle timer.
	m_flNextPoll = 0.0;
	return true;
}

bool CFrontressGCConnection::BAsyncSend( MsgType_t eMsg, const uint8 *pubMsgBytes, uint32 cubSize )
{
	return BSendRawMessage( eMsg, pubMsgBytes, cubSize );
}

//-----------------------------------------------------------------------------
void CFrontressGCConnection::Update()
{
	if ( !BEnabled() )
	{
		if ( m_bConnected )
		{
			Reset();
		}
		return;
	}

	if ( m_pPending != NULL )
		return;

	if ( Plat_FloatTime() < m_flNextPoll )
		return;

	Flush();
}

//-----------------------------------------------------------------------------
// Purpose: Send whatever is queued and ask for whatever is waiting. The same
//          request does both, so an idle client costs one long poll and a
//          client mid-queue costs nothing extra.
//-----------------------------------------------------------------------------
void CFrontressGCConnection::Flush()
{
	ISteamHTTP *pHTTP = GetHTTP();
	CSteamID steamID = GetLocalSteamID();
	if ( !pHTTP || !steamID.IsValid() )
	{
		m_flNextPoll = Plat_FloatTime() + k_flIdlePollInterval;
		return;
	}

	char szURL[ 512 ];
	V_snprintf( szURL, sizeof( szURL ), "%s/gc/v1/session", tf_gc_address.GetString() );

	HTTPRequestHandle hRequest = pHTTP->CreateHTTPRequest( k_EHTTPMethodPOST, szURL );
	if ( hRequest == INVALID_HTTPREQUEST_HANDLE )
	{
		m_flNextPoll = Plat_FloatTime() + k_flIdlePollInterval;
		return;
	}

	if ( !BSetAuthHeaders( hRequest ) )
	{
		pHTTP->ReleaseHTTPRequest( hRequest );
		m_flNextPoll = Plat_FloatTime() + k_flIdlePollInterval;
		return;
	}

	// Build the batch.
	CUtlBuffer bufBody;
	bufBody.PutUnsignedInt( FRONTRESS_GC_BATCH_MAGIC );
	bufBody.PutUnsignedInt( ( uint32 )m_vecOutbound.Count() );
	FOR_EACH_VEC( m_vecOutbound, i )
	{
		CUtlBuffer *pMsg = m_vecOutbound[ i ];
		bufBody.PutUnsignedInt( ( uint32 )pMsg->TellPut() );
		bufBody.Put( pMsg->Base(), pMsg->TellPut() );
	}

	pHTTP->SetHTTPRequestRawPostBody( hRequest, "application/octet-stream",
	                                  ( uint8 * )bufBody.Base(), bufBody.TellPut() );
	pHTTP->SetHTTPRequestNetworkActivityTimeout( hRequest, k_unLongPollSecs + 10 );

	SteamAPICall_t hCall;
	if ( !pHTTP->SendHTTPRequest( hRequest, &hCall ) )
	{
		pHTTP->ReleaseHTTPRequest( hRequest );
		SetConnected( false );
		m_flBackoff = MIN( k_flMaxBackoff, MAX( 1.0, m_flBackoff * 1.5 ) );
		m_flNextPoll = Plat_FloatTime() + m_flBackoff;
		return;
	}

	// The messages are in flight. Anything the coordinator does not like it
	// will tell us about; re-sending on failure would replay party operations.
	m_vecOutbound.PurgeAndDeleteElements();

	m_pPending = new CFrontressGCRequest( this, pHTTP, hRequest );
	m_pPending->m_CallResult.Set( hCall, m_pPending, &CFrontressGCRequest::OnCompleted );
}

//-----------------------------------------------------------------------------
bool CFrontressGCConnection::BSetAuthHeaders( HTTPRequestHandle hRequest )
{
	ISteamHTTP *pHTTP = GetHTTP();
	CSteamID steamID = GetLocalSteamID();

	char szSteamID[ 32 ];
	V_snprintf( szSteamID, sizeof( szSteamID ), "%llu", steamID.ConvertToUint64() );
	pHTTP->SetHTTPRequestHeaderValue( hRequest, "X-Frontress-SteamID", szSteamID );
	pHTTP->SetHTTPRequestHeaderValue( hRequest, "X-Frontress-Kind", BIsGameServer() ? "server" : "client" );

#ifdef GAME_DLL
	if ( engine->IsDedicatedServer() )
	{
		const char *pszToken = sv_private_token.GetString();
		if ( !pszToken || pszToken[0] == '\0' )
			return false;

		pHTTP->SetHTTPRequestHeaderValue( hRequest, "Authorization", ( CUtlString( "Token " ) + pszToken ).Get() );
		return true;
	}
#endif

	// A client signs with a Steam auth session ticket, which is the same thing
	// the coordinator already verifies against ISteamUserAuth. One ticket for
	// the life of the session.
	if ( m_sAuthTicket.IsEmpty() )
	{
		if ( !steamapicontext || !steamapicontext->SteamUser() || !steamapicontext->SteamUser()->BLoggedOn() )
			return false;

		uint8 rgubTicket[ 1024 ];
		uint32 cubTicket = 0;
		m_hAuthTicket = steamapicontext->SteamUser()->GetAuthSessionTicket( rgubTicket, sizeof( rgubTicket ), &cubTicket, NULL );
		if ( m_hAuthTicket == k_HAuthTicketInvalid || cubTicket == 0 )
			return false;

		char szHex[ 2 * sizeof( rgubTicket ) + 1 ];
		for ( uint32 i = 0; i < cubTicket; ++i )
		{
			V_snprintf( &szHex[ i * 2 ], 3, "%02x", rgubTicket[ i ] );
		}
		szHex[ cubTicket * 2 ] = '\0';
		m_sAuthTicket = szHex;
	}

	pHTTP->SetHTTPRequestHeaderValue( hRequest, "Authorization", ( CUtlString( "Steam " ) + m_sAuthTicket ).Get() );
	return true;
}

//-----------------------------------------------------------------------------
void CFrontressGCConnection::OnRequestFinished( CFrontressGCRequest *pRequest, bool bOK )
{
	if ( m_pPending == pRequest )
	{
		m_pPending = NULL;
	}

	if ( bOK )
	{
		SetConnected( true );
		m_flBackoff = 0.0;
		// Anything queued while the request was in flight goes out now;
		// otherwise hold the next long poll open after a short breath.
		m_flNextPoll = m_vecOutbound.Count() > 0 ? 0.0 : Plat_FloatTime() + 0.1;
	}
	else
	{
		SetConnected( false );
		m_flBackoff = MIN( k_flMaxBackoff, MAX( 1.0, m_flBackoff * 1.5 ) );
		m_flNextPoll = Plat_FloatTime() + m_flBackoff;
	}
}

//-----------------------------------------------------------------------------
void CFrontressGCConnection::OnBatchReceived( const uint8 *pubData, uint32 cubData )
{
	if ( cubData < 2 * sizeof( uint32 ) )
		return;	// an empty long poll: nothing was waiting

	CUtlBuffer buf( pubData, cubData, CUtlBuffer::READ_ONLY );
	if ( buf.GetUnsignedInt() != FRONTRESS_GC_BATCH_MAGIC )
	{
		Warning( "[FrontressGC] coordinator sent a batch we do not recognise\n" );
		return;
	}

	uint32 unCount = buf.GetUnsignedInt();
	for ( uint32 i = 0; i < unCount; ++i )
	{
		if ( buf.GetBytesRemaining() < ( int )sizeof( uint32 ) )
			break;

		uint32 cubMsg = buf.GetUnsignedInt();
		if ( cubMsg == 0 || ( int )cubMsg > buf.GetBytesRemaining() )
			break;

		DispatchMessage( ( const uint8 * )buf.PeekGet(), cubMsg );
		buf.SeekGet( CUtlBuffer::SEEK_CURRENT, cubMsg );
	}
}

//-----------------------------------------------------------------------------
// Purpose: Hand one GC message to the job manager, which is the same thing
//          CGCClient::OnGCMessageAvailable used to do before it was stubbed
//          out. The SO cache jobs the library registers at startup are what
//          pick up CSOTFParty, CSOTFGameServerLobby and the rest, so a lobby
//          arriving here builds a real lobby object and CMatchInfo behind it.
//-----------------------------------------------------------------------------
void CFrontressGCConnection::DispatchMessage( const uint8 *pubMsg, uint32 cubMsg )
{
	CNetPacket *pNetPacket = CNetPacketPool::AllocNetPacket();
	if ( !pNetPacket )
		return;

	pNetPacket->Init( cubMsg, pubMsg );

	IMsgNetPacket *pMsgNetPacket = IMsgNetPacketFromCNetPacket( pNetPacket );
	if ( !pMsgNetPacket )
	{
		pNetPacket->Release();
		return;
	}

	if ( tf_gc_debug.GetBool() )
	{
		Msg( "[FrontressGC] <- %s (%u bytes)\n", PchMsgNameFromEMsg( pMsgNetPacket->GetEMsg() ), cubMsg );
	}

	JobMsgInfo_t jobMsgInfo( pMsgNetPacket->GetEMsg(),
	                         pMsgNetPacket->GetSourceJobID(),
	                         pMsgNetPacket->GetTargetJobID(),
	                         k_EServerTypeGCClient );

	if ( !GCClientSystem()->GetGCClient()->GetJobMgr().BRouteMsgToJob( GCClientSystem()->GetGCClient(), pMsgNetPacket, jobMsgInfo ) )
	{
		if ( tf_gc_debug.GetBool() )
		{
			Warning( "[FrontressGC] nothing handles %s\n", PchMsgNameFromEMsg( pMsgNetPacket->GetEMsg() ) );
		}
	}

	pMsgNetPacket->Release();
	pNetPacket->Release();
}

//-----------------------------------------------------------------------------
void CFrontressGCConnection::SetConnected( bool bConnected )
{
	if ( m_bConnected == bConnected )
		return;

	m_bConnected = bConnected;
	Msg( "[FrontressGC] %s coordinator at %s\n", bConnected ? "connected to" : "lost", tf_gc_address.GetString() );
}

//-----------------------------------------------------------------------------
void CFrontressGCConnection::Reset()
{
	m_vecOutbound.PurgeAndDeleteElements();
	SetConnected( false );
	m_flBackoff = 0.0;
	m_flNextPoll = 0.0;
}

//-----------------------------------------------------------------------------
void CFrontressGCConnection::Shutdown()
{
	if ( m_pPending )
	{
		m_pPending->Orphan();
		m_pPending = NULL;
	}

	m_vecOutbound.PurgeAndDeleteElements();

#ifdef CLIENT_DLL
	if ( m_hAuthTicket != k_HAuthTicketInvalid && steamapicontext && steamapicontext->SteamUser() )
	{
		steamapicontext->SteamUser()->CancelAuthTicket( m_hAuthTicket );
		m_hAuthTicket = k_HAuthTicketInvalid;
	}
#endif
	m_sAuthTicket.Clear();
	m_bConnected = false;
}
