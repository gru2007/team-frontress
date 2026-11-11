//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: Discord Rich Presence and joins. See header.
//
// The protocol is Discord's RPC: a handshake with the application ID, then
// JSON commands (SET_ACTIVITY, SUBSCRIBE, SEND_ACTIVITY_JOIN_INVITE) and
// events (READY, ACTIVITY_JOIN, ACTIVITY_JOIN_REQUEST).
//
//=============================================================================//

#include "cbase.h"
#include "tier0/valve_minmax_off.h"
#include "fui_node.h"
#include "frontress_discord_ipc.h"
#include "tier0/valve_minmax_on.h"
#include "frontress_discord.h"
#include "fui_tf.h"
#include "igamesystem.h"
#include "inetchannelinfo.h"
#include "c_playerresource.h"
#include "c_tf_player.h"
#include "tf_gamerules.h"
#include "tf_gc_client.h"
#include "tf_partyclient.h"
#include "tf_party.h"
#include "tf_match_description.h"
#include "tf_matchmaking_shared.h"
#include "gc_clientsystem.h"
#include "clientsteamcontext.h"
#include "fmtstr.h"
#include "tier3/tier3.h"
#include <vgui/ILocalize.h>
#include <time.h>

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

// The Discord application (discord.com/developers/applications, General
// Information, Application ID). Not a secret. Empty: Discord stays off.
// -discord_app_id <id> overrides it, for testing another application.
#define FRONTRESS_DISCORD_APP_ID "892952070146117683"

static ConVar fr_discord( "fr_discord", "1", FCVAR_ARCHIVE, "Show what you are doing on your Discord profile (needs the Discord app)." );
static ConVar fr_discord_invites( "fr_discord_invites", "1", FCVAR_ARCHIVE,
	"Let Discord friends join your party or community server from your Discord profile, and accept their Ask to Join." );
static ConVar fr_discord_debug( "fr_discord_debug", "0", FCVAR_ARCHIVE, "Log Discord traffic." );

#define DiscordDbg( ... ) do { if ( fr_discord_debug.GetBool() ) Msg( "[discord] " __VA_ARGS__ ); } while ( 0 )

// Discord allows 5 activity updates per 20 seconds.
static const double k_flMinSendInterval = 4.0;
// A state has to hold this long before it is sent: loading screens and level
// changes pass through several in a second.
static const double k_flSettleTime = 1.5;
static const double k_flBuildInterval = 0.5;
static const double k_flReconnectInterval = 15.0;
static const double k_flHandshakeTimeout = 10.0;
// A join from Discord waits this long for the coordinator (Discord may have
// just started the game for it).
static const double k_flJoinWait = 120.0;
// How long a party let in by Discord may be asked to join.
static const double k_flApprovalTime = 300.0;
// Discord's limits on activity strings.
static const size_t k_nMaxText = 128;

using fui::Node;

//-----------------------------------------------------------------------------
// Strings
//-----------------------------------------------------------------------------

// Cut to at most nMax bytes without splitting a UTF-8 character.
static std::string Clip( std::string s, size_t nMax = k_nMaxText )
{
	if ( s.size() <= nMax )
		return s;
	size_t n = nMax;
	while ( n > 0 && ( (unsigned char)s[ n ] & 0xC0 ) == 0x80 )
		--n;
	s.resize( n );
	return s;
}

static std::string Utf8( const wchar_t *pwsz )
{
	if ( !pwsz )
		return std::string();
	char buf[ 512 ];
	V_UnicodeToUTF8( pwsz, buf, sizeof( buf ) );
	return buf;
}

// A token of resource/frontress_discord_<language>.txt (or any loaded file),
// with %s1 replaced.
static std::string Loc( const char *pszToken, const std::string &arg = std::string() )
{
	std::string s;
	if ( g_pVGuiLocalize )
		s = Utf8( g_pVGuiLocalize->Find( pszToken ) );
	if ( s.empty() )
		s = pszToken[ 0 ] == '#' ? pszToken + 1 : pszToken;
	const size_t at = s.find( "%s1" );
	if ( at != std::string::npos )
		s.replace( at, 3, arg );
	return s;
}

static std::string SteamIDString( const CSteamID &id )
{
	return CFmtStr( "%llu", (unsigned long long)id.ConvertToUint64() ).Get();
}

static bool BAllDigits( const char *psz, size_t nMax )
{
	size_t n = 0;
	for ( ; psz[ n ]; ++n )
	{
		if ( n >= nMax || psz[ n ] < '0' || psz[ n ] > '9' )
			return false;
	}
	return n > 0;
}

// "a.b.c.d:port" with nothing else in it. Only public IPv4 servers are
// offered on Discord: a LAN address means nothing to a friend elsewhere.
static bool BParseServerAddress( const char *psz, uint8 ip[ 4 ], int &nPort, bool bRequirePublic )
{
	unsigned a, b, c, d, port;
	char tail;
	if ( sscanf( psz, "%3u.%3u.%3u.%3u:%5u%c", &a, &b, &c, &d, &port, &tail ) != 5 )
		return false;
	if ( a > 255 || b > 255 || c > 255 || d > 255 || port == 0 || port > 65535 )
		return false;
	// The scan skips leading zeros and signs; require the canonical text.
	if ( V_strcmp( psz, CFmtStr( "%u.%u.%u.%u:%u", a, b, c, d, port ) ) != 0 )
		return false;
	if ( bRequirePublic )
	{
		const bool bPrivate = a == 0 || a == 10 || a == 127 || ( a == 100 && ( b & 0xC0 ) == 64 ) ||
			( a == 169 && b == 254 ) || ( a == 172 && ( b & 0xF0 ) == 16 ) || ( a == 192 && b == 168 ) || a >= 224;
		if ( bPrivate )
			return false;
	}
	ip[ 0 ] = (uint8)a; ip[ 1 ] = (uint8)b; ip[ 2 ] = (uint8)c; ip[ 3 ] = (uint8)d;
	nPort = (int)port;
	return true;
}

// Join secrets. Discord hands them to friends' games; never trusted as more
// than a party member's Steam ID or a server address.
#define FR_SECRET_PARTY		"fr1:p:"
#define FR_SECRET_SERVER	"fr1:s:"

//-----------------------------------------------------------------------------
// The activity: what the profile shows.
//-----------------------------------------------------------------------------
struct DiscordActivity_t
{
	// Which "session" this is: the start time resets when it changes.
	std::string session;
	std::string details;
	std::string state;
	std::string smallImage;
	std::string smallText;
	std::string partyId;
	int nPartySize = 0;
	int nPartyMax = 0;
	std::string joinSecret;

	bool operator==( const DiscordActivity_t &o ) const
	{
		return session == o.session && details == o.details && state == o.state && smallImage == o.smallImage &&
			smallText == o.smallText && partyId == o.partyId && nPartySize == o.nPartySize &&
			nPartyMax == o.nPartyMax && joinSecret == o.joinSecret;
	}
	bool operator!=( const DiscordActivity_t &o ) const { return !( *this == o ); }
};

//-----------------------------------------------------------------------------
class CFrontressDiscord : public CAutoGameSystemPerFrame
{
public:
	CFrontressDiscord() : CAutoGameSystemPerFrame( "CFrontressDiscord" ) {}

	virtual bool Init() OVERRIDE;
	virtual void Shutdown() OVERRIDE;
	virtual void Update( float frametime ) OVERRIDE;

	bool BApprovedJoinTarget( const CSteamID &steamID ) const
	{
		return m_approvedTarget.IsValid() && m_approvedTarget == steamID && Plat_FloatTime() < m_flApprovedUntil;
	}
	bool BTakesJoinRequestsFromStrangers() const
	{
		return m_eState == k_eReady && fr_discord_invites.GetBool() &&
			V_strncmp( m_sent.joinSecret.c_str(), FR_SECRET_PARTY, V_strlen( FR_SECRET_PARTY ) ) == 0;
	}
	void PrintStatus() const;

private:
	enum EState { k_eIdle, k_eHandshake, k_eReady };
	enum EJoin { k_eJoinNone, k_eJoinParty, k_eJoinServer };

	bool BWanted() const;
	void Connect( double flNow );
	void Disconnect( double flRetryAt, bool bClearActivity );
	void Pump( double flNow );
	void OnMessage( const std::string &json, double flNow );
	void Send( const char *pszCommand, Node &&args, const char *pszEvent = nullptr );
	void SendActivity( const DiscordActivity_t &a );
	void BuildActivity( DiscordActivity_t &a ) const;
	void OnJoin( const std::string &secret, double flNow );
	void OnJoinRequest( const Node &user );
	void RunPendingJoin( double flNow );

	CFrontressDiscordPipe m_pipe;
	std::string m_appId;
	bool m_bDisabledForSession = false;
	EState m_eState = k_eIdle;
	double m_flNextConnect = 0.0;
	double m_flHandshakeDeadline = 0.0;
	int m_nNonce = 0;
	std::string m_user;
	std::string m_lastError;
	std::string m_launch;
	bool m_bLaunchRegistered = false;

	// What was last sent, what is waiting to be, and since when.
	DiscordActivity_t m_sent;
	bool m_bSentAny = false;
	DiscordActivity_t m_candidate;
	double m_flCandidateSince = 0.0;
	double m_flNextBuild = 0.0;
	double m_flNextSend = 0.0;
	std::string m_session;
	int64 m_nSessionStart = 0;

	// A join from Discord, until the game can carry it out.
	EJoin m_eJoin = k_eJoinNone;
	CSteamID m_joinTarget;
	std::string m_joinServer;
	double m_flJoinDeadline = 0.0;

	CSteamID m_approvedTarget;
	double m_flApprovedUntil = 0.0;
};

static CFrontressDiscord s_FrontressDiscord;

bool FrontressDiscord_BApprovedJoinTarget( const CSteamID &steamID ) { return s_FrontressDiscord.BApprovedJoinTarget( steamID ); }
bool FrontressDiscord_BTakesJoinRequestsFromStrangers() { return s_FrontressDiscord.BTakesJoinRequestsFromStrangers(); }

//-----------------------------------------------------------------------------
bool CFrontressDiscord::Init()
{
	if ( g_pVGuiLocalize )
		g_pVGuiLocalize->AddFile( "resource/frontress_discord_%language%.txt", "GAME", true );

	m_appId = CommandLine()->ParmValue( "-discord_app_id", FRONTRESS_DISCORD_APP_ID );
	if ( !BAllDigits( m_appId.c_str(), 24 ) )
		m_appId.clear();
	if ( CommandLine()->FindParm( "-nodiscord" ) )
		m_bDisabledForSession = true;
	return true;
}

void CFrontressDiscord::Shutdown()
{
	Disconnect( 0.0, true );
}

bool CFrontressDiscord::BWanted() const
{
	return !m_bDisabledForSession && !m_appId.empty() && fr_discord.GetBool();
}

void CFrontressDiscord::Update( float )
{
	const double flNow = Plat_FloatTime();

	if ( !BWanted() )
	{
		if ( m_eState != k_eIdle )
			Disconnect( 0.0, true );
		return;
	}

	if ( m_eState == k_eIdle )
	{
		if ( flNow >= m_flNextConnect )
			Connect( flNow );
		if ( m_eState == k_eIdle )
			return;
	}

	Pump( flNow );

	if ( m_eState == k_eHandshake && flNow > m_flHandshakeDeadline )
	{
		m_lastError = "no answer to the handshake";
		Disconnect( flNow + k_flReconnectInterval, false );
		return;
	}
	if ( m_eState != k_eReady )
		return;

	RunPendingJoin( flNow );

	if ( flNow < m_flNextBuild )
		return;
	m_flNextBuild = flNow + k_flBuildInterval;

	DiscordActivity_t a;
	BuildActivity( a );
	if ( a != m_candidate )
	{
		m_candidate = a;
		m_flCandidateSince = flNow;
	}
	if ( ( !m_bSentAny || m_candidate != m_sent ) && flNow >= m_flNextSend && flNow - m_flCandidateSince >= k_flSettleTime )
	{
		SendActivity( m_candidate );
		m_flNextSend = flNow + k_flMinSendInterval;
	}
}

//-----------------------------------------------------------------------------
void CFrontressDiscord::Connect( double flNow )
{
	m_flNextConnect = flNow + k_flReconnectInterval;
	if ( !m_pipe.Open() )
		return; // Discord is not running; try again later, quietly

	Node hello;
	hello.SetObject();
	hello[ "v" ] = Node( 1 );
	hello[ "client_id" ] = Node( m_appId );
	if ( !m_pipe.Write( CFrontressDiscordPipe::k_eOp_Handshake, hello.Dump() ) )
		return;
	m_eState = k_eHandshake;
	m_flHandshakeDeadline = flNow + k_flHandshakeTimeout;
	DiscordDbg( "connected, handshake sent\n" );
}

void CFrontressDiscord::Disconnect( double flRetryAt, bool bClearActivity )
{
	if ( m_eState == k_eReady && bClearActivity )
	{
		Node args;
		args.SetObject();
		args[ "pid" ] = Node( CFrontressDiscordPipe::CurrentProcessId() );
		Send( "SET_ACTIVITY", std::move( args ) );
	}
	if ( m_eState != k_eIdle )
		DiscordDbg( "disconnected\n" );
	m_pipe.Close();
	m_eState = k_eIdle;
	m_flNextConnect = flRetryAt;
	m_bSentAny = false;
	m_sent = DiscordActivity_t();
	m_candidate = DiscordActivity_t();
}

void CFrontressDiscord::Pump( double flNow )
{
	uint32_t unOpcode = 0;
	std::string payload;
	while ( m_pipe.Read( unOpcode, payload ) )
	{
		switch ( unOpcode )
		{
		case CFrontressDiscordPipe::k_eOp_Frame:
			OnMessage( payload, flNow );
			break;
		case CFrontressDiscordPipe::k_eOp_Ping:
			m_pipe.Write( CFrontressDiscordPipe::k_eOp_Pong, payload );
			break;
		case CFrontressDiscordPipe::k_eOp_Close:
		{
			Node msg;
			Node::Parse( payload, msg );
			const Node *pCode = msg.Find( "code" );
			const Node *pMessage = msg.Find( "message" );
			m_lastError = pMessage ? pMessage->AsString() : payload;
			// 4000: this application ID is not Discord's. Retrying will not fix it.
			if ( pCode && (int)pCode->AsNumber() == 4000 )
			{
				Warning( "Discord: %s (application %s). Discord stays off.\n", m_lastError.c_str(), m_appId.c_str() );
				m_bDisabledForSession = true;
			}
			else
				DiscordDbg( "closed by Discord: %s\n", m_lastError.c_str() );
			Disconnect( flNow + k_flReconnectInterval, false );
			return;
		}
		default:
			break;
		}
	}
	if ( !m_pipe.BIsOpen() && m_eState != k_eIdle )
		Disconnect( flNow + k_flReconnectInterval, false );
}

void CFrontressDiscord::OnMessage( const std::string &json, double flNow )
{
	Node msg;
	if ( !Node::Parse( json, msg ) || !msg.IsObject() )
		return;
	const Node *pCmd = msg.Find( "cmd" );
	const Node *pEvt = msg.Find( "evt" );
	const Node *pData = msg.Find( "data" );
	const std::string cmd = pCmd ? pCmd->AsString() : std::string();
	const std::string evt = pEvt && !pEvt->IsNull() ? pEvt->AsString() : std::string();
	DiscordDbg( "<- %s\n", json.c_str() );

	if ( evt == "ERROR" )
	{
		const Node *pMessage = pData ? pData->Find( "message" ) : nullptr;
		m_lastError = cmd + ": " + ( pMessage ? pMessage->AsString() : std::string( "error" ) );
		DiscordDbg( "error: %s\n", m_lastError.c_str() );
		return;
	}
	if ( cmd != "DISPATCH" )
		return;

	if ( evt == "READY" )
	{
		m_eState = k_eReady;
		m_lastError.clear();
		const Node *pUser = pData ? pData->Find( "user" ) : nullptr;
		const Node *pName = pUser ? pUser->Find( "username" ) : nullptr;
		m_user = pName ? pName->AsString() : std::string();
		Msg( "Discord: connected%s%s\n", m_user.empty() ? "" : " as ", m_user.c_str() );

		Send( "SUBSCRIBE", Node(), "ACTIVITY_JOIN" );
		Send( "SUBSCRIBE", Node(), "ACTIVITY_JOIN_REQUEST" );

		if ( !m_bLaunchRegistered )
		{
			m_bLaunchRegistered = true;
			const unsigned int unSteamAppId = ( steamapicontext && steamapicontext->SteamUtils() ) ? steamapicontext->SteamUtils()->GetAppID() : 0;
			if ( !FrontressDiscord_RegisterLaunch( m_appId.c_str(), unSteamAppId, m_launch ) )
				DiscordDbg( "could not register the launch command\n" );
		}
		// Send what we are doing at once.
		m_flNextBuild = 0.0;
		m_flCandidateSince = flNow - k_flSettleTime;
		m_flNextSend = 0.0;
		return;
	}
	if ( evt == "ACTIVITY_JOIN" )
	{
		const Node *pSecret = pData ? pData->Find( "secret" ) : nullptr;
		if ( pSecret )
			OnJoin( pSecret->AsString(), flNow );
		return;
	}
	if ( evt == "ACTIVITY_JOIN_REQUEST" )
	{
		const Node *pUser = pData ? pData->Find( "user" ) : nullptr;
		if ( pUser )
			OnJoinRequest( *pUser );
		return;
	}
}

void CFrontressDiscord::Send( const char *pszCommand, Node &&args, const char *pszEvent )
{
	Node msg;
	msg.SetObject();
	msg[ "cmd" ] = Node( pszCommand );
	if ( args.IsObject() )
		msg[ "args" ] = std::move( args );
	else
		msg[ "args" ].SetObject();
	if ( pszEvent )
		msg[ "evt" ] = Node( pszEvent );
	msg[ "nonce" ] = Node( std::to_string( ++m_nNonce ) );
	const std::string json = msg.Dump();
	DiscordDbg( "-> %s\n", json.c_str() );
	m_pipe.Write( CFrontressDiscordPipe::k_eOp_Frame, json );
}

void CFrontressDiscord::SendActivity( const DiscordActivity_t &a )
{
	if ( a.session != m_session )
	{
		m_session = a.session;
		m_nSessionStart = (int64)time( nullptr );
	}

	Node activity;
	activity.SetObject();
	// Discord refuses strings shorter than 2 characters.
	if ( a.details.size() >= 2 )
		activity[ "details" ] = Node( Clip( a.details ) );
	if ( a.state.size() >= 2 )
		activity[ "state" ] = Node( Clip( a.state ) );
	activity[ "timestamps" ][ "start" ] = Node( (double)m_nSessionStart );

	Node &assets = activity[ "assets" ];
	assets[ "large_image" ] = Node( "logo" );
	assets[ "large_text" ] = Node( Clip( Loc( "#Frontress_Discord_LargeText" ) ) );
	if ( !a.smallImage.empty() )
	{
		assets[ "small_image" ] = Node( a.smallImage );
		if ( a.smallText.size() >= 2 )
			assets[ "small_text" ] = Node( Clip( a.smallText ) );
	}

	if ( !a.partyId.empty() && a.nPartyMax > 0 )
	{
		Node &party = activity[ "party" ];
		party[ "id" ] = Node( Clip( a.partyId ) );
		Node &size = party[ "size" ];
		size.Append( Node( a.nPartySize ) );
		size.Append( Node( a.nPartyMax ) );
	}
	if ( !a.joinSecret.empty() )
	{
		activity[ "secrets" ][ "join" ] = Node( Clip( a.joinSecret ) );
	}
	else
	{
		// Discord takes buttons or a join secret, not both.
		Node &button = activity[ "buttons" ].Append();
		button[ "label" ] = Node( Clip( Loc( "#Frontress_Discord_Website" ), 32 ) );
		button[ "url" ] = Node( "https://team-frontress.org" );
	}
	activity[ "instance" ] = Node( false );

	Node args;
	args.SetObject();
	args[ "pid" ] = Node( CFrontressDiscordPipe::CurrentProcessId() );
	args[ "activity" ] = std::move( activity );
	Send( "SET_ACTIVITY", std::move( args ) );

	m_sent = a;
	m_bSentAny = true;
}

//-----------------------------------------------------------------------------
// What the player is doing
//-----------------------------------------------------------------------------
void CFrontressDiscord::BuildActivity( DiscordActivity_t &a ) const
{
	CTFPartyClient *pParty = GTFPartyClient();
	ISteamUser *pSteamUser = steamapicontext ? steamapicontext->SteamUser() : nullptr;
	const CSteamID me = pSteamUser ? pSteamUser->GetSteamID() : CSteamID();
	const bool bInvites = fr_discord_invites.GetBool() && me.IsValid();

	INetChannelInfo *pNet = engine->GetNetChannelInfo();
	const bool bBackground = engine->IsLevelMainMenuBackground();
	const bool bConnected = engine->IsConnected() && !bBackground;
	const bool bLoopback = pNet && pNet->IsLoopback();
	// The main menu's level loads like any local game; until it says it is
	// the background, a local level that is still loading counts as menu.
	const bool bLocalLoading = bConnected && bLoopback && !engine->IsInGame();
	const bool bInMatch = GTFGCClientSystem() && GTFGCClientSystem()->BConnectedToMatchServer( false );

	// The map, as the menus name it.
	std::string map;
	if ( bConnected && engine->IsInGame() )
	{
		char szBase[ MAX_PATH ];
		V_FileBase( engine->GetLevelName(), szBase, sizeof( szBase ) );
		if ( szBase[ 0 ] )
		{
			const char *pszMap = GetMapDisplayName( szBase, true );
			map = pszMap && pszMap[ 0 ] ? pszMap : szBase;
		}
	}

	// The party, for the menu and matches: friends join it, as Steam's Join
	// Game does, never the match.
	auto UseParty = [ & ]()
	{
		const bool bHaveParty = pParty && pParty->BHaveActiveParty();
		a.nPartySize = bHaveParty ? Max( 1, pParty->CountNumOnlinePartyMembers() ) : 1;
		a.nPartyMax = MAX_PARTY_SIZE;
		if ( bHaveParty )
			a.partyId = CFmtStr( "party-%llu", (unsigned long long)pParty->GetActivePartyID() ).Get();
		else if ( me.IsValid() )
			a.partyId = "solo-" + SteamIDString( me );
		if ( bInvites && a.nPartySize < a.nPartyMax && !a.partyId.empty() )
			a.joinSecret = FR_SECRET_PARTY + SteamIDString( me );
	};

	// Class and team, once in the game.
	auto UseClass = [ & ]()
	{
		C_TFPlayer *pPlayer = C_TFPlayer::GetLocalTFPlayer();
		if ( !pPlayer )
			return;
		const int iTeam = pPlayer->GetTeamNumber();
		if ( iTeam == TEAM_SPECTATOR )
		{
			a.smallImage = "spectator";
			a.smallText = Loc( "#Frontress_Discord_Spectating" );
			return;
		}
		if ( iTeam != TF_TEAM_RED && iTeam != TF_TEAM_BLUE )
			return;
		const int iClass = pPlayer->GetPlayerClass() ? pPlayer->GetPlayerClass()->GetClassIndex() : TF_CLASS_UNDEFINED;
		const std::string team = Loc( iTeam == TF_TEAM_RED ? "#Frontress_Discord_TeamRed" : "#Frontress_Discord_TeamBlue" );
		if ( iClass >= TF_FIRST_NORMAL_CLASS && iClass <= TF_LAST_NORMAL_CLASS )
		{
			// Art assets class_<class>_red / _blue (docs/discord.md).
			a.smallImage = std::string( "class_" ) + g_aRawPlayerClassNamesShort[ iClass ] + ( iTeam == TF_TEAM_RED ? "_red" : "_blue" );
			a.smallText = Loc( g_aPlayerClassNames[ iClass ] ) + " \xC2\xB7 " + team;
		}
		else
		{
			a.smallImage = iTeam == TF_TEAM_RED ? "team_red" : "team_blue";
			a.smallText = team;
		}
	};

	if ( engine->IsPlayingDemo() )
	{
		a.session = "demo";
		a.details = Loc( "#Frontress_Discord_Demo" );
		a.state = map;
		return;
	}

	// A matchmade match (BConnectedToMatchServer includes connecting to it).
	if ( bInMatch )
	{
		const ETFMatchGroup eGroup = GTFGCClientSystem()->GetLiveMatchGroup();
		const IMatchGroupDescription *pDesc = GetMatchGroupDescription( eGroup );
		const char *pszName = pDesc ? pDesc->GetNameLocToken() : nullptr;
		a.session = CFmtStr( "match-%d", (int)eGroup ).Get();
		a.details = pszName ? Loc( "#Frontress_Discord_Match", Loc( pszName ) ) : Loc( "#Frontress_Discord_MatchGeneric" );
		a.state = map.empty() ? Loc( "#Frontress_Discord_Loading" ) : map;
		UseClass();
		UseParty();
		return;
	}

	// Any other server.
	if ( bConnected && !bLocalLoading )
	{
		if ( bLoopback )
		{
			a.session = "local";
			a.details = Loc( "#Frontress_Discord_Practice" );
			a.state = map;
			UseClass();
			return;
		}

		const char *pszAddress = pNet ? pNet->GetAddress() : "";
		a.session = std::string( "server-" ) + ( pszAddress ? pszAddress : "" );
		a.details = Loc( "#Frontress_Discord_Community" );
		a.state = map.empty() ? Loc( "#Frontress_Discord_Loading" ) : map;
		UseClass();

		// Friends join the server itself; its player count is the party.
		int nPlayers = 0;
		if ( g_PR && engine->IsInGame() )
		{
			for ( int i = 1; i <= gpGlobals->maxClients; ++i )
			{
				if ( g_PR->IsConnected( i ) && !g_PR->IsFakePlayer( i ) && !g_PR->IsHLTV( i ) )
					++nPlayers;
			}
		}
		uint8 ip[ 4 ];
		int nPort = 0;
		if ( nPlayers > 0 && pszAddress && BParseServerAddress( pszAddress, ip, nPort, true ) )
		{
			a.partyId = a.session;
			a.nPartySize = nPlayers;
			a.nPartyMax = Max( nPlayers, gpGlobals->maxClients );
			if ( bInvites && nPlayers < gpGlobals->maxClients )
				a.joinSecret = FR_SECRET_SERVER + std::string( pszAddress );
		}
		return;
	}

	// The menus.
	a.session = "menu";
	if ( pParty && ( pParty->BInStandbyQueue() || pParty->BInAnyMatchQueue() ) )
	{
		a.session = "search";
		const IMatchGroupDescription *pDesc = nullptr;
		if ( !pParty->BInStandbyQueue() && pParty->GetNumQueuedMatchGroups() == 1 )
			pDesc = GetMatchGroupDescription( pParty->GetQueuedMatchGroupByIdx( 0 ) );
		const char *pszName = pDesc ? pDesc->GetNameLocToken() : nullptr;
		a.details = pszName ? Loc( "#Frontress_Discord_SearchingGroup", Loc( pszName ) ) : Loc( "#Frontress_Discord_Searching" );
	}
	else
	{
		a.details = Loc( "#Frontress_Discord_MainMenu" );
	}
	a.state = Loc( "#Frontress_Discord_Party" );
	UseParty();
}

//-----------------------------------------------------------------------------
// Joins
//-----------------------------------------------------------------------------
void CFrontressDiscord::OnJoin( const std::string &secret, double flNow )
{
	if ( !fr_discord_invites.GetBool() )
	{
		Msg( "Discord: a join was ignored (fr_discord_invites is 0)\n" );
		return;
	}

	const char *psz = secret.c_str();
	const int nPartyPrefix = V_strlen( FR_SECRET_PARTY );
	const int nServerPrefix = V_strlen( FR_SECRET_SERVER );
	if ( V_strncmp( psz, FR_SECRET_PARTY, nPartyPrefix ) == 0 && BAllDigits( psz + nPartyPrefix, 20 ) )
	{
		const CSteamID target( (uint64)V_atoui64( psz + nPartyPrefix ) );
		ISteamUser *pSteamUser = steamapicontext ? steamapicontext->SteamUser() : nullptr;
		if ( !target.IsValid() || target.GetEAccountType() != k_EAccountTypeIndividual ||
			( pSteamUser && pSteamUser->GetSteamID() == target ) )
		{
			Warning( "Discord: ignoring a party join for %s\n", psz + nPartyPrefix );
			return;
		}
		m_eJoin = k_eJoinParty;
		m_joinTarget = target;
		m_flJoinDeadline = flNow + k_flJoinWait;
		DiscordDbg( "join party of %s\n", target.Render() );
		return;
	}
	uint8 ip[ 4 ];
	int nPort = 0;
	if ( V_strncmp( psz, FR_SECRET_SERVER, nServerPrefix ) == 0 && BParseServerAddress( psz + nServerPrefix, ip, nPort, true ) )
	{
		m_eJoin = k_eJoinServer;
		m_joinServer = CFmtStr( "%u.%u.%u.%u:%d", ip[ 0 ], ip[ 1 ], ip[ 2 ], ip[ 3 ], nPort ).Get();
		m_flJoinDeadline = flNow + k_flJoinWait;
		DiscordDbg( "join server %s\n", m_joinServer.c_str() );
		return;
	}
	Warning( "Discord: ignoring a join this game does not understand\n" );
}

void CFrontressDiscord::RunPendingJoin( double flNow )
{
	if ( m_eJoin == k_eJoinNone )
		return;
	if ( flNow > m_flJoinDeadline )
	{
		Warning( "Discord: gave up joining (no connection to the coordinator)\n" );
		m_eJoin = k_eJoinNone;
		return;
	}

	// Leaving a live match for a friend's server would abandon it.
	if ( m_eJoin == k_eJoinServer )
	{
		m_eJoin = k_eJoinNone;
		if ( GTFGCClientSystem() && GTFGCClientSystem()->BConnectedToMatchServer( true ) )
		{
			FuiTF().Toast( "party", "Discord", Loc( "#Frontress_Discord_LeaveMatchFirst" ), "" );
			return;
		}
		Msg( "Discord: joining %s\n", m_joinServer.c_str() );
		FuiTF().Toast( "party", "Discord", Loc( "#Frontress_Discord_JoiningServer" ), "" );
		engine->ClientCmd_Unrestricted( CFmtStr( "connect %s\n", m_joinServer.c_str() ) );
		return;
	}

	CTFPartyClient *pParty = GTFPartyClient();
	if ( !pParty || !GCClientSystem() || !GCClientSystem()->BConnectedtoGC() )
		return; // still starting up
	m_eJoin = k_eJoinNone;

	const CTFParty *pActive = pParty->GetActiveParty();
	if ( pActive && pActive->GetMemberIndexBySteamID( m_joinTarget ) != -1 )
		return; // already together

	// They let this player in on Discord; the party may take the request
	// although they are not Steam friends (see the header).
	m_approvedTarget = m_joinTarget;
	m_flApprovedUntil = flNow + k_flApprovalTime;
	if ( pParty->BRequestJoinPlayer( m_joinTarget, false ) )
	{
		Msg( "Discord: asked to join the party of %s\n", m_joinTarget.Render() );
		FuiTF().Toast( "party", "Discord", Loc( "#Frontress_Discord_JoiningParty" ), "" );
	}
	else
	{
		Warning( "Discord: could not ask to join the party of %s\n", m_joinTarget.Render() );
	}
}

void CFrontressDiscord::OnJoinRequest( const Node &user )
{
	const Node *pId = user.Find( "id" );
	const std::string id = pId ? pId->AsString() : std::string();
	if ( !BAllDigits( id.c_str(), 24 ) )
		return;
	const Node *pGlobal = user.Find( "global_name" );
	const Node *pName = user.Find( "username" );
	std::string name = pGlobal && !pGlobal->IsNull() ? pGlobal->AsString() : std::string();
	if ( name.empty() && pName )
		name = pName->AsString();

	Node args;
	args.SetObject();
	args[ "user_id" ] = Node( id );
	// Accepting only hands them the secret: a party still asks this player
	// before letting in someone who is not a Steam friend, and a community
	// server is open to anyone anyway.
	if ( fr_discord_invites.GetBool() && !m_sent.joinSecret.empty() )
	{
		Send( "SEND_ACTIVITY_JOIN_INVITE", std::move( args ) );
		Msg( "Discord: %s asked to join; accepted\n", name.c_str() );
		FuiTF().Toast( "party", "Discord", Loc( "#Frontress_Discord_JoinRequest", Clip( name, 64 ) ), "" );
	}
	else
	{
		Send( "CLOSE_ACTIVITY_REQUEST", std::move( args ) );
		Msg( "Discord: %s asked to join; declined (not joinable now)\n", name.c_str() );
	}
}

//-----------------------------------------------------------------------------
void CFrontressDiscord::PrintStatus() const
{
	if ( m_appId.empty() )
	{
		Msg( "Discord: no application ID in this build (FRONTRESS_DISCORD_APP_ID, or -discord_app_id)\n" );
		return;
	}
	Msg( "Discord: application %s, %s\n", m_appId.c_str(),
		!BWanted() ? ( m_bDisabledForSession ? "off for this session" : "off (fr_discord 0)" ) :
		m_eState == k_eReady ? "connected" : m_eState == k_eHandshake ? "connecting" : "Discord app not found (retrying)" );
	if ( m_eState == k_eReady && !m_user.empty() )
		Msg( "  user: %s\n", m_user.c_str() );
	if ( m_bSentAny )
	{
		Msg( "  shows: %s / %s\n", m_sent.details.c_str(), m_sent.state.c_str() );
		if ( m_sent.nPartyMax )
			Msg( "  party: %d of %d\n", m_sent.nPartySize, m_sent.nPartyMax );
		Msg( "  joinable: %s\n", m_sent.joinSecret.empty() ? "no" : m_sent.joinSecret.c_str() );
	}
	if ( !m_launch.empty() )
		Msg( "  started for joins by: %s\n", m_launch.c_str() );
	if ( !m_lastError.empty() )
		Msg( "  last error: %s\n", m_lastError.c_str() );
}

CON_COMMAND( fr_discord_status, "Show what Discord Rich Presence is doing." )
{
	s_FrontressDiscord.PrintStatus();
}
