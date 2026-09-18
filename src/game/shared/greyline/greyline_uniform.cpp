//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: see greyline_uniform.h.
//
//=========================================================================//

#include "cbase.h"
#include "greyline/greyline_uniform.h"
#include "tf_shareddefs.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

ConVar greyline_uniform_by_war_side( "greyline_uniform_by_war_side", "1",
	FCVAR_REPLICATED | FCVAR_NOTIFY,
	"Draw player bodies in the colours of the war side they fight for rather than of the "
	"team they play as, in battles where a directional map has put the two the wrong way "
	"round. Set it to 0 to keep the in-game team's colours everywhere, at the cost of a "
	"RED offensive being fought in BLU uniforms." );

ConVar greyline_uniform_swap( "greyline_uniform_swap", "0", FCVAR_REPLICATED,
	"Set by the server: 1 when this battle's roster has the war's sides in each other's "
	"in-game colours. Read greyline_uniform_by_war_side to decide whether to act on it." );

namespace greyline
{

//-----------------------------------------------------------------------------
bool UniformSwapped()
{
	return greyline_uniform_by_war_side.GetBool() && greyline_uniform_swap.GetBool();
}

//-----------------------------------------------------------------------------
int DisplayTeam( int iTeam )
{
	if ( !UniformSwapped() )
		return iTeam;

	// Per team, never per player: two people on the same team must look the
	// same as each other whatever the war thinks of them.
	if ( iTeam == TF_TEAM_RED )
		return TF_TEAM_BLUE;
	if ( iTeam == TF_TEAM_BLUE )
		return TF_TEAM_RED;
	return iTeam;
}

} // namespace greyline
