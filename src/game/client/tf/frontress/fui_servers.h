//========= Team Frontress ====================================================//
//
// Server pings for matchmaking (services/gc/internal/gc/probe.go). The
// coordinator sends the pool's servers (51021); the game pings each one
// through Steam's server queries and answers with what it measured (51022).
// A match only goes to a server every player in it reaches, so a player
// whose network cannot reach a host is never sent there.
//
// The same pings make the region picker on the play screen: region.list
// { id, name, ms, ok, measured, on }, region.pick and region.none_ok (no
// server answers in time; resource/fui/schema.json).
//
// A party no server suits within the ping limit plays on the best one there
// is; the coordinator says so (51023) and FuiOnUnreachable shows it: a toast,
// and reach (schema.json) under the searching button.
//
//=============================================================================
#ifndef FUI_SERVERS_H
#define FUI_SERVERS_H
#ifdef _WIN32
#pragma once
#endif

#include <string>
#include <vector>

namespace fui
{
	class Runtime;
}

// The coordinator's server list (JSON), from frontress_gc.cpp.
void FuiOnServerList( const char *pszJSON );
// No server suits the party within the ping limit, so it searches on the best
// one there is (51023): a toast and reach on the next menu refresh.
void FuiOnUnreachable( const char *pszJSON );
// Every menu refresh: the region picker.
void FuiServersUpdate( fui::Runtime &runtime );
// A document command; true if it was a region one.
bool FuiServersCommand( fui::Runtime &runtime, const std::string &name, const std::vector< std::string > &args );

#endif // FUI_SERVERS_H
