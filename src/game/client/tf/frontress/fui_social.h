//========= Team Frontress ====================================================//
//
// The menu's social side, from the coordinator (services/gc/internal/gc/
// social.go, clan.go, support.go): global, clan and party chat; who is
// online and who looks for a squad; the clan; the support tab and its Steam
// microtransactions. Two messages carry all of it: the game sends ops
// (51010), the coordinator sends events (51011), both JSON.
//
// State goes to the documents as social.*, clan.*, clanform.* and support.*
// (resource/fui/schema.json).
//
//=============================================================================
#ifndef FUI_SOCIAL_H
#define FUI_SOCIAL_H
#ifdef _WIN32
#pragma once
#endif

#include <string>
#include <vector>

namespace fui
{
	class Runtime;
}

// Every frame the menu updates (see CFuiTF::Update).
void FuiSocialUpdate( fui::Runtime &runtime, bool bShown );
// A document command; true if it was a social one.
bool FuiSocialCommand( fui::Runtime &runtime, const std::string &name, const std::vector< std::string > &args );
void FuiSocialNavigate( fui::Runtime &runtime, const std::string &screen );
// The coordinator's event message (JSON), from frontress_gc.cpp.
void FuiOnSocialEvent( const char *pszJSON );
// The menu is going away: stop listening to Steam.
void FuiSocialShutdown();

// The local player's clan tag ("" if none) and supporter badge, for
// nameplates. Party members' come with their looks.
const std::string &FuiSocialMyTag();
bool FuiSocialMySupporter();

#endif // FUI_SOCIAL_H
