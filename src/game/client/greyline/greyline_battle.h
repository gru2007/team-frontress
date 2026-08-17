//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: client half of a Greyline battle — hosting one, or getting into
//			somebody else's.
//
// There is no rendezvous layer here and there is not meant to be one. The
// coordinator says who hosts; that machine runs "map" and reports the address
// its own engine advertises; everyone else is handed that address and issues a
// single "connect". Under Steam Networking the address is a FakeIP standing in
// for a Steam P2P/SDR route, so this works from behind NAT without anybody
// forwarding a port — and without the game depending on Steam matchmaking,
// lobbies, or an AppID whose Steamworks configuration we do not control.
//
//=========================================================================//

#ifndef GREYLINE_BATTLE_H
#define GREYLINE_BATTLE_H
#ifdef _WIN32
#pragma once
#endif

#include "igamesystem.h"
#include "steam/steam_api.h"

//-----------------------------------------------------------------------------
// What the coordinator tells a client to do when it presses DEPLOY. This is the
// entire interface between the war layer and the networking layer.
//-----------------------------------------------------------------------------
struct GreylineBattleAssignment_t
{
	GreylineBattleAssignment_t()
	{
		szFront[0] = szBattle[0] = szMap[0] = szConnectAddress[0] = '\0';
		bBeHost = false;
		nMaxPlayers = 12;
		ulGameServerSteamID = 0;
		flDeadlineSeconds = 0.0f;
		bRestore = false;
		nRestoreRed = nRestoreBlu = nRestoreRounds = 0;
	}

	char	szFront[64];	// which front this battle belongs to
	char	szBattle[64];	// coordinator's battle id
	char	szMap[64];
	bool	bBeHost;		// the coordinator elected us; nobody self-promotes
	int		nMaxPlayers;

	// Where the battle is. Guests only, and filled straight from the host's own
	// report: an engine-advertised address first, its game server SteamID as the
	// fallback for a build that has no address to advertise.
	char	szConnectAddress[64];
	uint64	ulGameServerSteamID;

	// How long the coordinator will wait for us before it gives up and moves on.
	// Taken from the assignment so both sides run the same clock; 0 falls back to
	// the local convar.
	float	flDeadlineSeconds;

	// Score to put back when taking over a battle. Only the round scoreline
	// survives a change of host; see greyline_shared.h for why.
	bool	bRestore;
	int		nRestoreRed;
	int		nRestoreBlu;
	int		nRestoreRounds;
};

enum GreylineBattleState_t
{
	GREYLINE_IDLE = 0,
	GREYLINE_HOST_LOADING,	// we were elected; the map is loading
	GREYLINE_HOST_LIVE,		// our server is up and reported to the coordinator
	GREYLINE_CONNECTING,	// connect issued
	GREYLINE_IN_BATTLE,		// on the game server
	GREYLINE_HOLDING,		// host lost; waiting to be sent to its replacement
};

//-----------------------------------------------------------------------------
// Purpose: owns this client's relationship with one battle at a time.
//-----------------------------------------------------------------------------
class CGreylineBattle : public CAutoGameSystemPerFrame
{
public:
	CGreylineBattle();

	virtual void Update( float frametime ) OVERRIDE;

	// Entry point: act on a coordinator assignment.
	void Deploy( const GreylineBattleAssignment_t &assignment );

	// The host is gone and a replacement is booting. Sit still until the
	// coordinator sends us somewhere; going back to the menu now would turn a
	// loading screen into an abandoned battle.
	void Hold( float flSeconds, const char *pszMessage );

	// Leave whatever we are in and go back to idle.
	void Abort( const char *pszReason );

	// Current battle score, for the coordinator heartbeat. Read from the
	// replicated convars the server writes.
	void ReadScore( int &nRed, int &nBlu, int &nRounds ) const;

	GreylineBattleState_t	GetState() const	{ return m_eState; }
	const char *			GetStateName() const;
	const GreylineBattleAssignment_t &GetAssignment() const { return m_Assignment; }

private:
	void SetState( GreylineBattleState_t eState, float flTimeout );
	void StartHosting();
	bool LevelReady() const;
	void IssueScoreRestore();
	void PublishHost();
	void ConnectToBattle();

	GreylineBattleState_t		m_eState;
	GreylineBattleAssignment_t	m_Assignment;
	float						m_flStateDeadline;
	bool						m_bRestoreIssued;
};

CGreylineBattle *GreylineBattle();

#endif // GREYLINE_BATTLE_H
