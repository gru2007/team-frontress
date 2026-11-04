//========= Team Frontress ====================================================//
//
// 3D characters in the Frontress menu.
//
// A document marks where a character stands with an empty element:
//
//     <div class="hero" data-attr-fui-model="'me'"></div>
//
// and the stage puts a real TF2 player model (CTFPlayerModelPanel, the one
// the stock main menu used) in that box, in front of the background map and
// behind the rest of the menu. It follows the element every frame: layout,
// CSS transforms and animations, visibility. Who stands there is a "look",
// asked of fui_tf.cpp by key:
//
//     me                 the local player, current class, own loadout
//     class:<id>         the local player as another class (class select)
//     member:<steamid>   a party member, as the coordinator last said
//
// fui-model-anim="none" keeps a character still; anything else plays the
// class's entrance (the stock class_select scene) whenever the look changes.
//
//=============================================================================
#ifndef FUI_STAGE_H
#define FUI_STAGE_H
#ifdef _WIN32
#pragma once
#endif

#include "utlvector.h"
#include "utlstring.h"

namespace fui
{
	class Runtime;
}

// Who a character is: class, team colours and what they wear.
// One item worn, with what makes it look like itself (zero is "none"): a
// war paint and its wear, a paint can's colours, an unusual effect, a
// killstreak sheen, australium, a style, festive lights.
struct FuiLookItem_t
{
	uint16 iDef = 0;
	uint8 iQuality = 0;
	uint32 iPaintKit = 0;
	float flWear = 0.f;
	uint64 ulSeed = 0;
	uint32 iPaint = 0, iPaint2 = 0;
	uint32 iUnusual = 0;
	uint32 iSheen = 0;
	bool bAustralium = false;
	bool bFestive = false;
	uint8 iStyle = 0;
};

class CEconItemView;
// What an item looks like, read from it; and put back on a fresh one.
void FuiLookItemRead( const CEconItemView *pItem, FuiLookItem_t &out );
void FuiLookItemApply( const FuiLookItem_t &in, CEconItemView &item );

struct FuiLook_t
{
	FuiLook_t() : iClass( 0 ), iTeam( 2 ), bLocalLoadout( false ) {}

	int iClass;
	int iTeam;								// TF_TEAM_RED / TF_TEAM_BLUE
	bool bLocalLoadout;						// wear the local player's own items
	CUtlVector< FuiLookItem_t > vecItems;	// otherwise these items

	uint32 Hash() const;
};

class CFuiStage;

// Created with the UI, under the client panel (the UI itself is a popup
// above it).
CFuiStage *FuiStageCreate( uintp parentVPanel );	// a vgui::VPANEL
void FuiStageDestroy( CFuiStage *pStage );
// Each frame, after the runtime has laid out.
void FuiStageUpdate( CFuiStage *pStage, fui::Runtime &runtime, bool bShown );
// Keep the stage directly under the UI panel.
void FuiStageRaise( CFuiStage *pStage );

#endif // FUI_STAGE_H
