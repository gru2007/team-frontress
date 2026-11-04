//========= Team Frontress ====================================================//
//
// The lobby's world: the main menu's background level is a real TF2 map
// (scripts/chapterbackgrounds.txt), and the menu's camera stands where the
// map's own spectator cameras stand.
//
// Picking the spot is the server's job, because only the server has the
// map's entities (most TF2 maps compress the entity lump; the engine unpacks
// it when it loads the level). A background level runs a local server, which
// looks through info_observer_point entities, takes a scenic one (not the
// match summary podium), and hands it over in a replicated convar. The
// client's view then sits there, drifting a little so the world breathes.
//
//=============================================================================
#include "cbase.h"

#ifdef GAME_DLL
#include "tf_player.h"
#endif

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

// "x y z pitch yaw roll fov", or empty when the level has no spot to offer.
ConVar tf_lobby_camera( "tf_lobby_camera", "", FCVAR_REPLICATED | FCVAR_HIDDEN, "Where the main menu's camera stands in the background level (set by the server)." );

#ifdef GAME_DLL
//-----------------------------------------------------------------------------
class CLobbyWorldServer : public CAutoGameSystem
{
public:
	CLobbyWorldServer() : CAutoGameSystem( "CLobbyWorldServer" ) {}

	virtual void LevelInitPostEntity() OVERRIDE
	{
		tf_lobby_camera.SetValue( "" );
		if ( gpGlobals->eLoadType != MapLoad_Background )
			return;

		// Every usable spot, the welcome view first; the podium room behind
		// the map (match_summary) is never a view of the map.
		CUtlVector< CObserverPoint * > spots;
		CObserverPoint *pWelcome = NULL;
		for ( CBaseEntity *pEnt = gEntList.FindEntityByClassname( NULL, "info_observer_point" ); pEnt;
			  pEnt = gEntList.FindEntityByClassname( pEnt, "info_observer_point" ) )
		{
			CObserverPoint *pPoint = dynamic_cast< CObserverPoint * >( pEnt );
			if ( !pPoint || pPoint->IsMatchSummary() )
				continue;
			if ( pPoint->IsDefaultWelcome() )
				pWelcome = pPoint;
			spots.AddToTail( pPoint );
		}
		if ( !spots.Count() )
			return;

		RandomSeed( (int)Plat_MSTime() );
		CObserverPoint *pSpot = ( pWelcome && RandomInt( 0, 1 ) ) ? pWelcome : spots[ RandomInt( 0, spots.Count() - 1 ) ];
		const Vector &o = pSpot->GetAbsOrigin();
		const QAngle &a = pSpot->GetAbsAngles();
		const float flFOV = pSpot->m_flFOV > 10.f ? pSpot->m_flFOV : 75.f;
		tf_lobby_camera.SetValue( CFmtStr( "%.1f %.1f %.1f %.1f %.1f %.1f %.1f", o.x, o.y, o.z, a.x, a.y, a.z, flFOV ) );
		DevMsg( "Lobby world: %s, camera at %s\n", STRING( gpGlobals->mapname ), tf_lobby_camera.GetString() );
	}
};
static CLobbyWorldServer s_LobbyWorldServer;
#endif // GAME_DLL

#ifdef CLIENT_DLL
#include "view_shared.h"

ConVar fui_lobby_drift( "fui_lobby_drift", "1", FCVAR_ARCHIVE, "Let the main menu's camera drift slowly over the background level." );

//-----------------------------------------------------------------------------
// Puts the view where the server said, while the background level is up.
// Returns false (leave the view alone) in a real match.
//-----------------------------------------------------------------------------
bool FrontressLobbyView( CViewSetup *pSetup )
{
	if ( !engine->IsLevelMainMenuBackground() )
		return false;

	float v[ 7 ];
	if ( sscanf( tf_lobby_camera.GetString(), "%f %f %f %f %f %f %f", &v[ 0 ], &v[ 1 ], &v[ 2 ], &v[ 3 ], &v[ 4 ], &v[ 5 ], &v[ 6 ] ) != 7 )
		return false;

	Vector origin( v[ 0 ], v[ 1 ], v[ 2 ] );
	QAngle angles( v[ 3 ], v[ 4 ], v[ 5 ] );
	if ( fui_lobby_drift.GetBool() )
	{
		// A slow look around and a breath of movement: never still, never
		// enough to notice as motion.
		const float t = (float)Plat_FloatTime();
		angles.y += 4.f * sinf( t * 0.045f );
		angles.x += 1.2f * sinf( t * 0.031f + 1.f );
		Vector forward, right, up;
		AngleVectors( angles, &forward, &right, &up );
		origin += right * ( 18.f * sinf( t * 0.027f ) ) + up * ( 4.f * sinf( t * 0.05f ) );
	}
	pSetup->origin = origin;
	pSetup->angles = angles;
	pSetup->fov = v[ 6 ];
	return true;
}
#endif // CLIENT_DLL
