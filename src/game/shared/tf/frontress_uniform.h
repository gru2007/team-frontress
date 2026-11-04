#ifndef FRONTRESS_UNIFORM_H
#define FRONTRESS_UNIFORM_H
#ifdef _WIN32
#pragma once
#endif

namespace frontress
{
// Presentation only: map rules, damage and entity teams retain their real team.
// The authenticated GC assignment sets the replicated mapping for the match.
bool UniformSwapped();
int DisplayTeam( int iTeam );
}
#endif
