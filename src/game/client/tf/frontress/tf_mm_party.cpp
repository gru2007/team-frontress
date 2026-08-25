//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The party, as a Steam lobby.
//
// Adapted from Momentum Mod's mom_lobby_system.cpp, which solves the same
// problem for the same reason: the game's own lobby service is a Valve backend
// nobody else can run, and the public Steam matchmaking API is right there.
//
// What we keep from that design: the lobby is the single source of truth for
// membership, Steam's own callbacks drive every state change, and the lobby's
// key/value data carries whatever the game needs the members to agree on.
//
// What is different: Momentum's lobby is the session -- players are in it while
// they play. Ours is only the party. The match itself happens on a dedicated
// server the coordinator reserves, so the lobby's job ends when the assignment
// arrives.
//
//=============================================================================//

#include "cbase.h"

#include "tf_mm_backend.h"

#include "clientmode_tf.h"
#include "clientsteamcontext.h"
#include "fmtstr.h"
#include "tf_gc_client.h"
#include "tf_partyclient.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

ConVar tf_mm_party_type( "tf_mm_party_type", "-1", FCVAR_ARCHIVE,
                         "Override who may join your matchmaking party, as a Steam lobby type: "
                         "0 = invite only, 1 = friends, 2 = public. -1 follows the party settings "
                         "panel's own invite-mode dropdown, which is what a player sees.",
                         true, -1, true, 2 );
ConVar tf_mm_debug( "tf_mm_debug", "0", FCVAR_NONE,
                    "Spew what the matchmaking backend is doing." );
ConVar tf_mm_party_autocreate( "tf_mm_party_autocreate", "1", FCVAR_ARCHIVE,
                               "Host a matchmaking party lobby as soon as matchmaking comes up, so friends can "
                               "invite you and join you from Steam without you doing anything first." );

// Lobby data keys. These are a protocol between our own clients, so treat them
// as one: adding a key is free, changing what a key means is not.
#define TFMM_LOBBY_DATA_LEADER     "leader"
#define TFMM_LOBBY_DATA_QUEUEGROUP "queue_group"
#define TFMM_LOBBY_DATA_CONNECT    "connect"
#define TFMM_LOBBY_DATA_PASSWORD   "password"
#define TFMM_LOBBY_DATA_MATCHID    "match_id"
// Where the leader is playing when it is not a matchmade match. See
// CTFMMBackend::PublishPartyServer.
#define TFMM_LOBBY_DATA_SERVER     "server"

// The rich presence key that carries our party lobby. A friend's client reads
// it to answer "join this player" without anything in the middle.
#define TFMM_RP_KEY_LOBBY          "tf_lobby"
// And the one that carries the SourceTV relay of the match we are in, so a
// friend can watch it. Spectating is the one thing a stranger to the match can
// always be allowed to do: the relay is a separate server with its own slots,
// and letting somebody watch costs the match nothing.
#define TFMM_RP_KEY_STV            "tf_stv"

static ISteamMatchmaking *MM()
{
	return steamapicontext ? steamapicontext->SteamMatchmaking() : NULL;
}

static CSteamID LocalSteamID()
{
	if ( steamapicontext && steamapicontext->SteamUser() )
		return steamapicontext->SteamUser()->GetSteamID();
	return CSteamID();
}

//-----------------------------------------------------------------------------
CTFMMParty::CTFMMParty()
	: m_lobbyID( k_steamIDNil )
	, m_bHosting( false )
	, m_eAppliedLobbyType( k_ELobbyTypeFriendsOnly )
{
	m_szConnectString[0] = '\0';
}

//-----------------------------------------------------------------------------
void CTFMMParty::Create()
{
	if ( BValid() )
	{
		Warning( "You are already in a matchmaking party.\n" );
		return;
	}
	if ( m_callLobbyCreated.IsActive() )
		return;

	ISteamMatchmaking *pMM = MM();
	if ( !pMM )
	{
		Warning( "Steam matchmaking is not available; cannot create a party.\n" );
		return;
	}

	SteamAPICall_t call = pMM->CreateLobby( WantedLobbyType(), k_nTFMMMaxPartyMembers );
	m_callLobbyCreated.Set( call, this, &CTFMMParty::OnCreated );
}

//-----------------------------------------------------------------------------
void CTFMMParty::OnCreated( LobbyCreated_t *pCreated, bool bIOFailure )
{
	if ( bIOFailure || !pCreated )
	{
		Warning( "Could not create a matchmaking party: Steam did not answer.\n" );
		return;
	}
	if ( pCreated->m_eResult != k_EResultOK )
	{
		Warning( "Could not create a matchmaking party (result %d).\n", pCreated->m_eResult );
		return;
	}

	m_lobbyID = CSteamID( pCreated->m_ulSteamIDLobby );
	m_bHosting = true;
	m_eAppliedLobbyType = WantedLobbyType();

	// Steam's lobby owner and our idea of the party leader must agree, and the
	// UI reads the leader from the party object, not from Steam. Publish it.
	SetLobbyData( TFMM_LOBBY_DATA_LEADER, CFmtStr( "%llu", LocalSteamID().ConvertToUint64() ) );
	UpdateRichPresence();

	if ( tf_mm_debug.GetBool() )
		Msg( "[mm] party lobby %llu created\n", m_lobbyID.ConvertToUint64() );
}

//-----------------------------------------------------------------------------
// Purpose: Who Steam should let into our party lobby.
//
//			There were two convars for this and only one of them did anything.
//			The dashboard's settings panel writes tf_party_join_request_mode --
//			that is the dropdown a player actually sees -- and nothing read it,
//			because the lobby was created from tf_mm_party_type. So the visible
//			control was inert and the working one was hidden.
//
//			The dropdown wins. tf_mm_party_type is still honoured when somebody
//			set it deliberately (-1 is "follow the dropdown", which is its new
//			default), because a public lobby is a thing a community server
//			operator may genuinely want and the dropdown cannot ask for it.
//
//			Steam has no "they must ask first" state, so open and
//			request-to-join are both friends-only: the difference between them
//			is whether the stock client auto-accepts, which is above this.
//-----------------------------------------------------------------------------
ELobbyType CTFMMParty::WantedLobbyType()
{
	const int nOverride = tf_mm_party_type.GetInt();
	if ( nOverride >= 0 )
		return (ELobbyType)nOverride;

	if ( GTFPartyClient() &&
	     GTFPartyClient()->GetPartyJoinRequestMode() == CTFPartyClient::k_ePartyJoinRequestMode_ClosedToFriends )
	{
		return k_ELobbyTypePrivate; // invite only
	}
	return k_ELobbyTypeFriendsOnly;
}

//-----------------------------------------------------------------------------
// Purpose: Keep Steam's idea of who may join in step with the player's.
//
//			Called every frame from the backend; a lobby type that has not
//			changed is not re-sent, and only the owner may set it at all.
//-----------------------------------------------------------------------------
void CTFMMParty::ApplyJoinPolicy()
{
	if ( !BValid() || !MM() || !BIsLeader() )
		return;

	const ELobbyType eWanted = WantedLobbyType();
	if ( eWanted == m_eAppliedLobbyType )
		return;

	m_eAppliedLobbyType = eWanted;
	MM()->SetLobbyType( m_lobbyID, eWanted );

	if ( tf_mm_debug.GetBool() )
		Msg( "[mm] party lobby type is now %d\n", (int)eWanted );
}

//-----------------------------------------------------------------------------
void CTFMMParty::Leave()
{
	if ( !BValid() )
		return;

	if ( MM() )
		MM()->LeaveLobby( m_lobbyID );

	if ( tf_mm_debug.GetBool() )
		Msg( "[mm] left party lobby %llu\n", m_lobbyID.ConvertToUint64() );

	m_lobbyID = k_steamIDNil;
	m_bHosting = false;
	UpdateRichPresence();
}

//-----------------------------------------------------------------------------
bool CTFMMParty::BTryJoin( const CSteamID &lobbyID )
{
	if ( !MM() )
		return false;

	if ( m_lobbyID == lobbyID )
		return false;

	if ( m_callLobbyJoined.IsActive() )
	{
		Warning( "Already joining a party.\n" );
		return false;
	}

	// Leaving first keeps us out of the state where Steam thinks we are in two
	// lobbies and the party object has to guess which one is ours.
	if ( BValid() )
		Leave();

	SteamAPICall_t call = MM()->JoinLobby( lobbyID );
	m_callLobbyJoined.Set( call, this, &CTFMMParty::OnJoined );
	return true;
}

//-----------------------------------------------------------------------------
bool CTFMMParty::BTryJoinFromString( const char *pszID )
{
	if ( !pszID || !pszID[0] )
		return false;

	uint64 ulID = Q_atoui64( pszID );
	if ( ulID == 0 )
	{
		Warning( "'%s' is not a lobby id.\n", pszID );
		return false;
	}

	CSteamID lobbyID;
	lobbyID.FullSet( ulID, GetUniverse(), k_EAccountTypeChat );
	return BTryJoin( lobbyID );
}

//-----------------------------------------------------------------------------
void CTFMMParty::OnJoined( LobbyEnter_t *pEntered, bool bIOFailure )
{
	// The callback below does the work; this only exists so a failed call
	// result is not silent.
	if ( bIOFailure || !pEntered )
		Warning( "Could not join that party: Steam did not answer.\n" );
}

//-----------------------------------------------------------------------------
void CTFMMParty::OnLobbyEnter( LobbyEnter_t *pEnter )
{
	if ( pEnter->m_EChatRoomEnterResponse != k_EChatRoomEnterResponseSuccess )
	{
		Warning( "Could not join that party (response %d).\n", pEnter->m_EChatRoomEnterResponse );
		return;
	}

	m_lobbyID = CSteamID( pEnter->m_ulSteamIDLobby );
	m_bHosting = ( GetLeader() == LocalSteamID() );
	UpdateRichPresence();

	// Joining somebody mid-queue or mid-match: whatever they have published is
	// what we should be doing too -- including a plain server they are on,
	// which is what entering a lobby arms a follow for.
	TFMMBackend()->OnPartyLobbyEntered();

	if ( tf_mm_debug.GetBool() )
		Msg( "[mm] entered party lobby %llu (%d members)\n", m_lobbyID.ConvertToUint64(), GetNumMembers() );
}

//-----------------------------------------------------------------------------
void CTFMMParty::OnLobbyChatUpdate( LobbyChatUpdate_t *pParam )
{
	if ( CSteamID( pParam->m_ulSteamIDLobby ) != m_lobbyID )
		return;

	const uint32 state = pParam->m_rgfChatMemberStateChange;
	if ( ( state & ( k_EChatMemberStateChangeLeft | k_EChatMemberStateChangeDisconnected ) ) &&
	     CSteamID( pParam->m_ulSteamIDUserChanged ) == LocalSteamID() )
	{
		// We were kicked or dropped.
		m_lobbyID = k_steamIDNil;
		m_bHosting = false;
		UpdateRichPresence();
		return;
	}

	// Steam moves the lobby owner when the old one leaves. If that is us now,
	// say so, because the party object's leader comes from lobby data.
	if ( MM() && BValid() && MM()->GetLobbyOwner( m_lobbyID ) == LocalSteamID() )
	{
		m_bHosting = true;
		SetLobbyData( TFMM_LOBBY_DATA_LEADER, CFmtStr( "%llu", LocalSteamID().ConvertToUint64() ) );
	}

	UpdateRichPresence();
}

//-----------------------------------------------------------------------------
void CTFMMParty::OnLobbyDataUpdate( LobbyDataUpdate_t *pParam )
{
	if ( !pParam->m_bSuccess || CSteamID( pParam->m_ulSteamIDLobby ) != m_lobbyID )
		return;

	if ( tf_mm_debug.GetBool() )
	{
		Msg( "[mm] party data updated (lobby %llu, member %llu)\n",
		     (unsigned long long)pParam->m_ulSteamIDLobby, (unsigned long long)pParam->m_ulSteamIDMember );
	}

	// The leader publishes the match here. This is how a party member finds out
	// where to connect without ever talking to the coordinator.
	TFMMBackend()->OnPartyLobbyDataChanged();
}

//-----------------------------------------------------------------------------
void CTFMMParty::OnLobbyChatMsg( LobbyChatMsg_t *pParam )
{
	if ( !MM() || CSteamID( pParam->m_ulSteamIDLobby ) != m_lobbyID )
		return;

	char szText[1024] = { 0 };
	CSteamID speaker;
	EChatEntryType eType = k_EChatEntryTypeInvalid;
	int nRead = MM()->GetLobbyChatEntry( m_lobbyID, pParam->m_iChatID, &speaker,
	                                     szText, sizeof( szText ) - 1, &eType );
	if ( nRead <= 0 || eType != k_EChatEntryTypeChatMsg )
		return;

	szText[ MIN( nRead, (int)sizeof( szText ) - 1 ) ] = '\0';

	// The stock party chat panel listens for this event.
	IGameEvent *pEvent = gameeventmanager ? gameeventmanager->CreateEvent( "party_chat" ) : NULL;
	if ( pEvent )
	{
		pEvent->SetString( "steamid", CFmtStr( "%llu", speaker.ConvertToUint64() ) );
		pEvent->SetString( "text", szText );
		pEvent->SetInt( "type", k_eTFPartyChatType_MemberChat );
		gameeventmanager->FireEventClientSide( pEvent );
	}
}

//-----------------------------------------------------------------------------
void CTFMMParty::OnJoinRequested( GameLobbyJoinRequested_t *pJoin )
{
	// Accepting an invite from the Steam overlay or a command line.
	BTryJoin( pJoin->m_steamIDLobby );
}

//-----------------------------------------------------------------------------
bool CTFMMParty::BIsLeader() const
{
	if ( !BValid() )
		return true; // a party of one is led by its only member
	return GetLeader() == LocalSteamID();
}

//-----------------------------------------------------------------------------
CSteamID CTFMMParty::GetLeader() const
{
	if ( !BValid() || !MM() )
		return LocalSteamID();

	const char *pszLeader = GetLobbyData( TFMM_LOBBY_DATA_LEADER );
	if ( pszLeader && pszLeader[0] )
	{
		uint64 ulLeader = Q_atoui64( pszLeader );
		if ( ulLeader != 0 )
			return CSteamID( ulLeader );
	}

	// No leader published yet: Steam's owner is the best answer available.
	return MM()->GetLobbyOwner( m_lobbyID );
}

//-----------------------------------------------------------------------------
uint64 CTFMMParty::GetPartyID() const
{
	// The party id the UI shows and logs is the lobby's account id. A solo
	// player has no lobby and no party id, which is what the stock client
	// expects to see when it is not in a party.
	return BValid() ? m_lobbyID.ConvertToUint64() : 0ull;
}

//-----------------------------------------------------------------------------
int CTFMMParty::GetNumMembers() const
{
	if ( !BValid() || !MM() )
		return 1;
	return MM()->GetNumLobbyMembers( m_lobbyID );
}

//-----------------------------------------------------------------------------
CSteamID CTFMMParty::GetMember( int i ) const
{
	if ( !BValid() || !MM() )
		return LocalSteamID();
	return MM()->GetLobbyMemberByIndex( m_lobbyID, i );
}

//-----------------------------------------------------------------------------
void CTFMMParty::SetLobbyData( const char *pszKey, const char *pszValue )
{
	if ( !BValid() || !MM() )
		return;
	MM()->SetLobbyData( m_lobbyID, pszKey, pszValue );
}

//-----------------------------------------------------------------------------
const char *CTFMMParty::GetLobbyData( const char *pszKey ) const
{
	if ( !BValid() || !MM() )
		return "";
	return MM()->GetLobbyData( m_lobbyID, pszKey );
}

//-----------------------------------------------------------------------------
void CTFMMParty::SendChat( const char *pszText )
{
	if ( !pszText || !pszText[0] )
		return;

	if ( BValid() && MM() )
	{
		// Steam echoes the message back to us as well, so this is the only
		// place it needs to be said.
		MM()->SendLobbyChatMsg( m_lobbyID, pszText, V_strlen( pszText ) + 1 );
		return;
	}

	// No lobby: talking to yourself. Bounce it locally so the chat box still
	// behaves, which is what the stock client does for a party of one.
	IGameEvent *pEvent = gameeventmanager ? gameeventmanager->CreateEvent( "party_chat" ) : NULL;
	if ( pEvent )
	{
		pEvent->SetString( "steamid", CFmtStr( "%llu", LocalSteamID().ConvertToUint64() ) );
		pEvent->SetString( "text", pszText );
		pEvent->SetInt( "type", k_eTFPartyChatType_MemberChat );
		gameeventmanager->FireEventClientSide( pEvent );
	}
}

//-----------------------------------------------------------------------------
// Steam rich presence.
//
// Two things depend on this. The friends list shows what we are doing, which
// the stock client already builds in clientmode_tf.cpp. And "Join Game" runs
// whatever we put in the connect string -- which is how a friend gets into our
// party with no coordinator, no invite and no GC anywhere in the path.
//-----------------------------------------------------------------------------
void CTFMMParty::UpdateRichPresence() const
{
	if ( !steamapicontext || !steamapicontext->SteamFriends() )
		return;

	if ( BValid() )
	{
		steamapicontext->SteamFriends()->SetRichPresence(
			TFMM_RP_KEY_LOBBY, CFmtStr( "%llu", m_lobbyID.ConvertToUint64() ) );
	}
	else
	{
		steamapicontext->SteamFriends()->SetRichPresence( TFMM_RP_KEY_LOBBY, NULL );
	}

	// The relay for the match we are in, if there is one. Cleared the moment
	// there is not, so a friend is never pointed at a match that finished.
	const char *pszSTV = TFMMBackend()->GetSTVAddress();
	steamapicontext->SteamFriends()->SetRichPresence(
		TFMM_RP_KEY_STV, ( pszSTV && pszSTV[0] ) ? pszSTV : NULL );

	// The connect string itself belongs to clientmode_tf.cpp, which owns every
	// rich presence key and rewrites them together. Two writers would race and
	// the loser's value would stick until the next unrelated event.
	if ( GetClientModeTFNormal() )
		GetClientModeTFNormal()->MarkRichPresenceDirty();
}

//-----------------------------------------------------------------------------
const char *CTFMMParty::GetJoinConnectString() const
{
	if ( !BValid() )
		return NULL;

	V_snprintf( m_szConnectString, sizeof( m_szConnectString ),
	            "+connect_lobby %llu", (unsigned long long)m_lobbyID.ConvertToUint64() );
	return m_szConnectString;
}

//-----------------------------------------------------------------------------
// Purpose: The SourceTV relay of the match a friend is in, or NULL.
//
//			Same shape as GetFriendPartyLobby and for the same reason: the
//			answer is in their rich presence, so nothing has to broker it and
//			it works for anybody on the friends list, in our party or not.
//-----------------------------------------------------------------------------
const char *CTFMMParty::GetFriendSTV( const CSteamID &friendID )
{
	if ( !steamapicontext || !steamapicontext->SteamFriends() )
		return NULL;

	const char *pszSTV = steamapicontext->SteamFriends()->GetFriendRichPresence( friendID, TFMM_RP_KEY_STV );
	return ( pszSTV && pszSTV[0] ) ? pszSTV : NULL;
}

//-----------------------------------------------------------------------------
CSteamID CTFMMParty::GetFriendPartyLobby( const CSteamID &friendID )
{
	if ( !steamapicontext || !steamapicontext->SteamFriends() )
		return k_steamIDNil;

	const char *pszLobby = steamapicontext->SteamFriends()->GetFriendRichPresence( friendID, TFMM_RP_KEY_LOBBY );
	if ( !pszLobby || !pszLobby[0] )
		return k_steamIDNil;

	const uint64 ulLobby = Q_atoui64( pszLobby );
	if ( ulLobby == 0 )
		return k_steamIDNil;

	CSteamID lobbyID( ulLobby );
	return lobbyID.IsLobby() ? lobbyID : k_steamIDNil;
}

//-----------------------------------------------------------------------------
void CTFMMParty::OpenInviteDialog()
{
	if ( !steamapicontext || !steamapicontext->SteamFriends() )
		return;

	if ( !BValid() )
	{
		// Inviting somebody is a perfectly clear request for a party. Make one
		// rather than telling the player to run a console command first; the
		// dialog opens on the next frame that has a lobby.
		Create();
		Msg( "Creating a party -- open the invite dialog again in a moment.\n" );
		return;
	}

	steamapicontext->SteamFriends()->ActivateGameOverlayInviteDialog( m_lobbyID );
}

//
// Console commands. The stock UI drives most of this, but a party is easier to
// test from the console than from three panels.
//

CON_COMMAND( tf_mm_party_create, "Create a matchmaking party others can be invited to." )
{
	TFMMBackend()->Party().Create();
}

CON_COMMAND( tf_mm_party_leave, "Leave your matchmaking party." )
{
	TFMMBackend()->Party().Leave();
}

CON_COMMAND( tf_mm_party_invite, "Open the Steam invite dialog for your matchmaking party." )
{
	TFMMBackend()->Party().OpenInviteDialog();
}

CON_COMMAND( tf_mm_party_join, "Join a matchmaking party by lobby id." )
{
	if ( args.ArgC() < 2 )
	{
		Msg( "Usage: tf_mm_party_join <lobby_id>\n" );
		return;
	}
	TFMMBackend()->Party().BTryJoinFromString( args.Arg( 1 ) );
}

// Steam runs this one when a lobby invite is accepted while the game is not
// running yet; the name is the one Steam has used since Left 4 Dead.
CON_COMMAND( connect_lobby, "Join the matchmaking party in the given Steam lobby." )
{
	if ( args.ArgC() < 2 )
		return;
	TFMMBackend()->Party().BTryJoinFromString( args.Arg( 1 ) );
}
