//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: client half of Greyline battle hosting — the Steam lobby around a
//			battle, and getting the player into the game server behind it.
//
//=========================================================================//

#ifndef GREYLINE_LOBBY_H
#define GREYLINE_LOBBY_H
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
		szFront[0] = szBattle[0] = szMap[0] = '\0';
		bBeHost = false;
		nMaxPlayers = 12;
		ulLobbyID = 0;
		bRestore = false;
		nRestoreRed = nRestoreBlu = nRestoreRounds = 0;
	}

	char	szFront[64];	// which front this battle belongs to
	char	szBattle[64];	// coordinator's battle id; the lobby search key
	char	szMap[64];		// only meaningful for the host
	bool	bBeHost;		// the coordinator elected us; nobody self-promotes
	int		nMaxPlayers;
	// Non-zero when the battle already has a lobby and we are taking over as
	// host. Steam keeps the lobby alive when its owner leaves, so a migration
	// reuses the room the other players are already sitting in.
	uint64	ulLobbyID;

	// Score to put back when taking over a battle. Only the round scoreline
	// survives a change of host; see greyline_shared.h for why.
	bool	bRestore;
	int		nRestoreRed;
	int		nRestoreBlu;
	int		nRestoreRounds;
};

enum GreylineLobbyState_t
{
	GREYLINE_IDLE = 0,
	GREYLINE_SEARCHING,		// RequestLobbyList in flight
	GREYLINE_JOINING,		// JoinLobby in flight
	GREYLINE_IN_LOBBY,		// in the lobby, no game server published yet
	GREYLINE_CREATING,		// CreateLobby in flight
	GREYLINE_HOST_LOADING,	// we own the lobby and are loading the map
	GREYLINE_HOST_LIVE,		// our server is published to the lobby
	GREYLINE_CONNECTING,	// connect issued
	GREYLINE_IN_BATTLE,		// on the game server
};

//-----------------------------------------------------------------------------
// Purpose: owns the client's relationship with one battle lobby at a time.
//-----------------------------------------------------------------------------
class CGreylineLobby : public CAutoGameSystemPerFrame
{
public:
	CGreylineLobby();

	virtual bool Init() OVERRIDE;
	virtual void Update( float frametime ) OVERRIDE;

	// Entry point: act on a coordinator assignment.
	void Deploy( const GreylineBattleAssignment_t &assignment );

	// Leave whatever we are in and go back to idle.
	void Abort( const char *pszReason );

	// Join a specific lobby by id. Used by the Steam invite paths, which arrive
	// both as a callback and as a "+connect_lobby" console command.
	void JoinLobbyByID( CSteamID lobby );

	// Hand the lobby to the coordinator's chosen host. Only the current owner
	// can do this, and Steam's automatic owner after a host leaves is arbitrary,
	// so whoever finds itself holding the lobby passes it to the right player.
	void YieldLobbyOwnership( CSteamID steamIDNewHost );

	// Current battle score, for the coordinator heartbeat. Read from the
	// replicated convars the server writes.
	void ReadScore( int &nRed, int &nBlu, int &nRounds ) const;

	GreylineLobbyState_t	GetState() const	{ return m_eState; }
	const char *			GetStateName() const;
	CSteamID				GetLobby() const	{ return m_Lobby; }
	const GreylineBattleAssignment_t &GetAssignment() const { return m_Assignment; }

private:
	void SetState( GreylineLobbyState_t eState );
	// bLeaveLobby is false when the next thing we do is take that same lobby
	// over, which would otherwise leave the room we are about to host in.
	void Reset( const char *pszReason, bool bLeaveLobby );

	void SearchForBattle();
	void CreateBattleLobby();
	void TakeOverBattle();
	void StartHosting();
	bool OwnsLobby() const;
	void PublishGameServer();
	bool LevelReady() const;
	void IssueScoreRestore();
	void ConnectTo( uint32 unIP, uint16 usPort, CSteamID steamIDServer );
	void WriteLobbyMetadata();

	// Steam call results.
	void OnLobbyMatchList( LobbyMatchList_t *pResult, bool bIOFailure );
	CCallResult< CGreylineLobby, LobbyMatchList_t > m_callLobbyMatchList;

	void OnLobbyCreated( LobbyCreated_t *pResult, bool bIOFailure );
	CCallResult< CGreylineLobby, LobbyCreated_t > m_callLobbyCreated;

	void OnLobbyEntered( LobbyEnter_t *pResult, bool bIOFailure );
	CCallResult< CGreylineLobby, LobbyEnter_t > m_callLobbyEntered;

	// Steam callbacks.
	STEAM_CALLBACK( CGreylineLobby, OnLobbyGameCreated, LobbyGameCreated_t, m_callbackLobbyGameCreated );
	STEAM_CALLBACK( CGreylineLobby, OnGameLobbyJoinRequested, GameLobbyJoinRequested_t, m_callbackJoinRequested );

	GreylineLobbyState_t		m_eState;
	CSteamID					m_Lobby;
	GreylineBattleAssignment_t	m_Assignment;
	float						m_flStateDeadline;
	bool						m_bServerPublished;
	bool						m_bRestoreIssued;
};

CGreylineLobby *GreylineLobby();

#endif // GREYLINE_LOBBY_H
