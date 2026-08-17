//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: server half of Greyline battle hosting.
//
// Three jobs, all small:
//   1. Apply the hosting contract before players can arrive.
//   2. Publish where this server is — the address the engine advertises, plus
//      its Steam identity — so the host's own client can hand that to the
//      coordinator and everyone else can simply connect to it.
//   3. Keep the battle's round score visible to the client, and put it back
//      after a migration.
//
// It deliberately does no networking. The engine's Steam Networking support
// already makes a listen server reachable; nothing here needs to help it.
//
//=========================================================================//

#include "cbase.h"
#include "greyline/greyline_shared.h"
#include "igamesystem.h"
#include "eiface.h"
#include "iserver.h"
#include "team.h"
#include "tf_shareddefs.h"
#include "tf_gamerules.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

ConVar greyline_host_debug( "greyline_host_debug", "0", FCVAR_NONE,
	"Spew Greyline hosting contract and battle server endpoint progress." );

//-----------------------------------------------------------------------------
// Purpose: applies the hosting contract and waits for the game server identity.
//-----------------------------------------------------------------------------
class CGreylineHost : public CAutoGameSystemPerFrame
{
public:
	CGreylineHost() : CAutoGameSystemPerFrame( "CGreylineHost" )
	{
		m_bContractApplied = false;
		m_bIdentityPublished = false;
		m_flNextIdentityCheck = 0.0f;
		m_flNextScorePublish = 0.0f;
		m_bRestorePending = false;
		m_nRestoreRed = 0;
		m_nRestoreBlu = 0;
		m_nRestoreRounds = 0;
		m_flRestoreDeadline = 0.0f;
	}

	void QueueRestore( int nRed, int nBlu, int nRounds )
	{
		m_bRestorePending = true;
		m_nRestoreRed = nRed;
		m_nRestoreBlu = nBlu;
		m_nRestoreRounds = nRounds;
		m_flRestoreDeadline = gpGlobals->curtime + 30.0f;
	}

	virtual void LevelInitPostEntity() OVERRIDE;
	virtual void LevelShutdownPostEntity() OVERRIDE;
	virtual void FrameUpdatePostEntityThink() OVERRIDE;

	// Writes the current round score into the replicated convars the host's
	// client reads. Cheap, but not free, so it runs on a timer rather than
	// every tick.
	void PublishScore();

	// Puts a migrated battle's score back. Returns false if the teams are not
	// ready yet, in which case the caller retries.
	bool RestoreScore( int nRed, int nBlu, int nRounds );

	// Applies every convar in the contract. Returns false if a required one
	// could not be set, in which case the caller must not host.
	bool ApplyHostContract();

private:
	// Returns false while the address is still being allocated, which is the one
	// thing worth waiting for before declaring this server reachable.
	bool TryPublishAddress();
	void TryPublishEndpoint();

	bool	m_bContractApplied;
	bool	m_bIdentityPublished;
	float	m_flNextIdentityCheck;
	float	m_flNextScorePublish;

	// A restore requested before the teams existed, retried each frame until it
	// takes. A migrated battle that silently starts 0-0 is worse than one that
	// starts late.
	bool	m_bRestorePending;
	int		m_nRestoreRed;
	int		m_nRestoreBlu;
	int		m_nRestoreRounds;
	float	m_flRestoreDeadline;
};

static CGreylineHost g_GreylineHost;

//-----------------------------------------------------------------------------
bool CGreylineHost::ApplyHostContract()
{
	bool bOK = true;

	for ( int i = 0; i < g_nGreylineHostContractCount; ++i )
	{
		const GreylineConVarSetting_t &setting = g_GreylineHostContract[i];

		// These live in the engine, not in the game DLLs, so ConVarRef would
		// not find them at static init time.
		ConVar *pVar = g_pCVar->FindVar( setting.pszName );
		if ( !pVar )
		{
			if ( setting.bRequired )
			{
				Warning( "[greyline] required convar %s does not exist in this engine build; refusing to host\n",
					setting.pszName );
				bOK = false;
			}
			else if ( greyline_host_debug.GetBool() )
			{
				Msg( "[greyline] optional convar %s not present, skipping\n", setting.pszName );
			}
			continue;
		}

		pVar->SetValue( setting.pszValue );

		// Read it back. A convar can refuse a value it considers out of range,
		// and silently hosting outside the contract is worse than not hosting.
		const char *pszActual = pVar->GetString();
		if ( V_stricmp( pszActual, setting.pszValue ) != 0 )
		{
			// Numeric convars normalise ("0" -> "0.0"), so compare as floats
			// before deciding it really failed.
			if ( V_atof( pszActual ) != V_atof( setting.pszValue ) ||
				 ( setting.pszValue[0] == '\0' ) != ( pszActual[0] == '\0' ) )
			{
				Warning( "[greyline] %s is %s after being set to %s\n",
					setting.pszName, pszActual, setting.pszValue );
				if ( setting.bRequired )
				{
					bOK = false;
				}
				continue;
			}
		}

		if ( greyline_host_debug.GetBool() )
		{
			Msg( "[greyline] %s = %s\n", setting.pszName, pVar->GetString() );
		}
	}

	m_bContractApplied = bOK;
	return bOK;
}

//-----------------------------------------------------------------------------
void CGreylineHost::LevelInitPostEntity()
{
	m_bIdentityPublished = false;
	m_flNextIdentityCheck = 0.0f;
	m_flNextScorePublish = 0.0f;
	greyline_server_id.SetValue( "0" );
	greyline_server_addr.SetValue( "" );

	// A fresh level starts at zero. A migration overwrites this moments later
	// through greyline_restore_score; leaving the previous battle's numbers
	// here would make a brand new battle look like a continuation.
	greyline_score_red.SetValue( 0 );
	greyline_score_blu.SetValue( 0 );
	greyline_rounds_played.SetValue( 0 );

	// A dedicated server is configured by its own cfg files; only a listen
	// server needs the contract forced on it, and only there does the host's
	// own client share this process.
	if ( engine->IsDedicatedServer() )
	{
		return;
	}

	ApplyHostContract();
}

//-----------------------------------------------------------------------------
void CGreylineHost::LevelShutdownPostEntity()
{
	m_bIdentityPublished = false;
	greyline_server_id.SetValue( "0" );
	greyline_server_addr.SetValue( "" );
}

//-----------------------------------------------------------------------------
// Purpose: the game server logs on to Steam and allocates its address
//			asynchronously, so neither is available at level init. Poll until
//			they are.
//-----------------------------------------------------------------------------
void CGreylineHost::FrameUpdatePostEntityThink()
{
	// A pending restore is retried every frame: the window between the teams
	// existing and the first player connecting is short, and missing it means
	// the battle resumes at the wrong score.
	if ( m_bRestorePending )
	{
		if ( RestoreScore( m_nRestoreRed, m_nRestoreBlu, m_nRestoreRounds ) )
		{
			m_bRestorePending = false;
		}
		else if ( gpGlobals->curtime > m_flRestoreDeadline )
		{
			m_bRestorePending = false;
			Warning( "[greyline] gave up restoring the battle score; it will resume at 0-0\n" );
		}
	}

	if ( gpGlobals->curtime >= m_flNextScorePublish )
	{
		m_flNextScorePublish = gpGlobals->curtime + 1.0f;
		PublishScore();
	}


	if ( m_bIdentityPublished || engine->IsDedicatedServer() )
	{
		return;
	}

	if ( gpGlobals->curtime < m_flNextIdentityCheck )
	{
		return;
	}
	m_flNextIdentityCheck = gpGlobals->curtime + 0.5f;

	TryPublishEndpoint();
}

//-----------------------------------------------------------------------------
// Purpose: is this mode's state actually the team scoreline?
//
// CTeamplayRoundBasedRules::SetWinningTeam adds to the team score once per
// round when ShouldScorePerRound() is set, so for arena, KOTH, CP, payload and
// attack/defend the team score really is the match. Two modes it is not:
//
//   Mann vs. Machine — the match is the wave number, the credits earned and
//     every upgrade bought. Team score means nothing there.
//   Tournament / stopwatch — the time to beat and which side attacks are part
//     of the match; a restored score without them describes a different game.
//
// The coordinator already refuses to migrate battles whose map is marked
// non-migratable. This is the second line: it catches a map pool that says one
// thing and a map that turns out to be another.
//-----------------------------------------------------------------------------
static bool BattleStateIsTeamScore( const char *&pszWhyNot )
{
	if ( !TFGameRules() )
	{
		pszWhyNot = "game rules are not up";
		return false;
	}
	if ( TFGameRules()->IsMannVsMachineMode() )
	{
		pszWhyNot = "Mann vs. Machine keeps its state in waves, credits and upgrades";
		return false;
	}
	if ( TFGameRules()->IsInTournamentMode() )
	{
		pszWhyNot = "tournament and stopwatch formats carry more than a scoreline";
		return false;
	}
	return true;
}

//-----------------------------------------------------------------------------
void CGreylineHost::PublishScore()
{
	CTeam *pRed = GetGlobalTeam( TF_TEAM_RED );
	CTeam *pBlu = GetGlobalTeam( TF_TEAM_BLUE );
	if ( !pRed || !pBlu )
	{
		return;
	}

	// Publishing a scoreline for a mode whose state is not the scoreline would
	// hand the coordinator a snapshot that looks usable and is not.
	const char *pszWhyNot = "";
	if ( !BattleStateIsTeamScore( pszWhyNot ) )
	{
		return;
	}

	// Do not publish while a restore is still queued, or the client would
	// briefly report 0-0 for a battle that is mid-migration and the coordinator
	// would record that as the snapshot.
	if ( m_bRestorePending )
	{
		return;
	}

	greyline_score_red.SetValue( pRed->GetScore() );
	greyline_score_blu.SetValue( pBlu->GetScore() );

	if ( TFGameRules() )
	{
		greyline_rounds_played.SetValue( TFGameRules()->GetRoundsPlayed() );
	}
}

//-----------------------------------------------------------------------------
bool CGreylineHost::RestoreScore( int nRed, int nBlu, int nRounds )
{
	CTeam *pRed = GetGlobalTeam( TF_TEAM_RED );
	CTeam *pBlu = GetGlobalTeam( TF_TEAM_BLUE );
	if ( !pRed || !pBlu )
	{
		return false; // teams not spawned yet
	}

	const char *pszWhyNot = "";
	if ( !BattleStateIsTeamScore( pszWhyNot ) )
	{
		if ( !TFGameRules() )
		{
			return false; // too early; retry
		}
		// Refusing loudly beats resuming a battle that quietly lost most of
		// itself. The coordinator sees the host fail and abandons the match.
		Warning( "[greyline] refusing to restore a score in this mode: %s\n", pszWhyNot );
		return true; // handled: stop retrying
	}

	pRed->SetScore( nRed );
	pBlu->SetScore( nBlu );

	greyline_score_red.SetValue( nRed );
	greyline_score_blu.SetValue( nBlu );
	greyline_rounds_played.SetValue( nRounds );

	Msg( "[greyline] battle resumed at RED %d - BLU %d after %d round(s)\n",
		nRed, nBlu, nRounds );
	return true;
}

//-----------------------------------------------------------------------------
// Purpose: write the address guests should connect to, if there is one.
//
// Returns false while the answer is still coming — a FakeIP allocation is
// asynchronous, and publishing before it lands would hand out an invalid
// address. This mirrors what TF2's own server does before it tells the GC it is
// ready to host a match.
//
// Which addresses are worth publishing:
//
//   FakeIP           always. It is the Steam P2P/SDR route, which is the whole
//                    reason a player-hosted battle is reachable at all.
//   public IP        yes. A host with a real routable address does not need a
//                    relay, and saying so costs nothing.
//   LAN / loopback   only with sv_lan, where that is exactly what testers want.
//                    Otherwise it is worse than no address: it looks usable and
//                    reaches nobody, and it would beat the SteamID fallback.
//-----------------------------------------------------------------------------
bool CGreylineHost::TryPublishAddress()
{
	IServer *pServer = engine->GetIServer();
	if ( !pServer )
	{
		return true; // nothing to wait for; the SteamID is all this build has
	}

	const bool bFakeIP = pServer->IsUsingFakeIP();
	const netadr_t adr = pServer->GetPublicAddress();

	if ( !adr.IsValid() )
	{
		// A FakeIP that has not arrived yet is worth waiting for. Anything else
		// invalid is simply not going to appear.
		return !bFakeIP;
	}

	static ConVarRef sv_lan( "sv_lan" );
	const bool bUsable = bFakeIP || !adr.IsReservedAdr() ||
		( sv_lan.IsValid() && sv_lan.GetBool() );

	if ( !bUsable )
	{
		if ( greyline_host_debug.GetBool() )
		{
			Msg( "[greyline] not advertising %s: a private address reaches nobody outside this network\n",
				adr.ToString() );
		}
		return true;
	}

	char szAddress[64];
	adr.ToString_safe( szAddress );
	greyline_server_addr.SetValue( szAddress );

	Msg( "[greyline] battle server address %s%s\n", szAddress,
		bFakeIP ? " (Steam Networking)" : "" );
	return true;
}

//-----------------------------------------------------------------------------
void CGreylineHost::TryPublishEndpoint()
{
	const CSteamID *pSteamID = engine->GetGameServerSteamID();
	if ( !pSteamID || !pSteamID->IsValid() || pSteamID->GetAccountID() == 0 )
	{
		return;
	}

	// The address is the half guests actually use, so do not report an endpoint
	// until it has either arrived or been ruled out.
	if ( !TryPublishAddress() )
	{
		return;
	}

	char szID[32];
	V_snprintf( szID, sizeof( szID ), "%llu", pSteamID->ConvertToUint64() );
	greyline_server_id.SetValue( szID );
	m_bIdentityPublished = true;

	// Said once per level, and worth saying loudly: without an address the only
	// thing to hand out is a Steam identity, and reaching a server by identity
	// alone is not a path this build has ever been proven to have.
	if ( !greyline_server_addr.GetString()[0] )
	{
		Warning( "[greyline] this server advertises no address; remote players may not be able to reach it.\n" );
		Warning( "[greyline] the engine only requests a Steam FakeIP when the game is launched with -enablefakeip.\n" );
	}

	Msg( "[greyline] game server identity %s ready%s\n", szID,
		m_bContractApplied ? "" : " (WARNING: hosting contract was not fully applied)" );
}

//-----------------------------------------------------------------------------
// Purpose: restore a migrated battle's score.
//
// Issued by the host's own client once its map has finished loading. This is a
// server ConCommand without FCVAR_CLIENTCMD_CAN_EXECUTE, so a connected client
// cannot run it — only the local console of the machine running the server can,
// which on a listen server is the host, and on a dedicated server is its
// operator.
//-----------------------------------------------------------------------------
CON_COMMAND_F( greyline_restore_score, "Restore a migrated battle's score: greyline_restore_score <red> <blu> <rounds>", FCVAR_GAMEDLL )
{
	if ( args.ArgC() < 4 )
	{
		Msg( "usage: greyline_restore_score <red> <blu> <rounds>\n" );
		return;
	}

	const int nRed = V_atoi( args[1] );
	const int nBlu = V_atoi( args[2] );
	const int nRounds = V_atoi( args[3] );

	if ( nRed < 0 || nBlu < 0 || nRounds < 0 )
	{
		Warning( "[greyline] refusing a negative restore score\n" );
		return;
	}

	// Try immediately; if the teams are not up yet, retry from the frame loop.
	if ( !g_GreylineHost.RestoreScore( nRed, nBlu, nRounds ) )
	{
		g_GreylineHost.QueueRestore( nRed, nBlu, nRounds );
	}
}

//-----------------------------------------------------------------------------
// Purpose: lets the host apply the contract without reloading the map, and
//			report what the engine actually ended up with.
//-----------------------------------------------------------------------------
CON_COMMAND_F( greyline_host_status, "Show the Greyline hosting contract state on this server.", FCVAR_GAMEDLL )
{
	Msg( "greyline hosting contract:\n" );
	for ( int i = 0; i < g_nGreylineHostContractCount; ++i )
	{
		const GreylineConVarSetting_t &setting = g_GreylineHostContract[i];
		ConVar *pVar = g_pCVar->FindVar( setting.pszName );
		Msg( "  %-48s want %-6s have %s\n",
			setting.pszName,
			setting.pszValue[0] ? setting.pszValue : "\"\"",
			pVar ? ( pVar->GetString()[0] ? pVar->GetString() : "\"\"" ) : "<missing>" );
	}
	Msg( "  server address: %s\n",
		greyline_server_addr.GetString()[0] ? greyline_server_addr.GetString() : "<none advertised>" );
	Msg( "  game server id: %s\n", greyline_server_id.GetString() );
	Msg( "  score         : RED %s - BLU %s, %s round(s) played\n",
		greyline_score_red.GetString(), greyline_score_blu.GetString(),
		greyline_rounds_played.GetString() );
}
