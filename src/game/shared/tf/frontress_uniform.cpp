#include "cbase.h"
#include "tf/frontress_uniform.h"
#include "tf_shareddefs.h"
#include "tier0/memdbgon.h"

ConVar tf_war_uniform_swap( "tf_war_uniform_swap", "0", FCVAR_REPLICATED | FCVAR_HIDDEN,
	"Campaign faction colours assigned by the Frontress GC for this match." );

namespace frontress
{
bool UniformSwapped() { return tf_war_uniform_swap.GetBool(); }
int DisplayTeam( int team )
{
	if ( !UniformSwapped() ) return team;
	if ( team == TF_TEAM_RED ) return TF_TEAM_BLUE;
	if ( team == TF_TEAM_BLUE ) return TF_TEAM_RED;
	return team;
}
}
