//========= Team Frontress ====================================================//
#ifndef TF_MATCH_STATS_H
#define TF_MATCH_STATS_H
#ifdef _WIN32
#pragma once
#endif

#include "tf_gamerules.h"

// Arena runs its entire combat round in STALEMATE; sudden death is live
// gameplay as well. Neither should be mistaken for setup or warmup.
inline bool IsTFMatchGameplayActive( CTFGameRules *pRules )
{
	return pRules && !pRules->IsInWaitingForPlayers() &&
		( pRules->State_Get() == GR_STATE_RND_RUNNING || pRules->State_Get() == GR_STATE_STALEMATE );
}

#endif // TF_MATCH_STATS_H
