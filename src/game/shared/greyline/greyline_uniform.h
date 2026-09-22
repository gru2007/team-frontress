//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: whether this battle has the two teams wearing each other's
// colours, and what colour a given team should therefore be drawn in.
//
// The war has sides — RED and BLU fight a campaign — and the game has teams,
// and the AttackerTeam rule deliberately lets them disagree: the attacking
// side always wears BLU, because every payload and attack/defend map ever
// built is BLU-attacks and no convar changes the cart or the spawn doors. So
// a RED offensive is fought in BLU uniforms, and a player who only looks at
// their own colours has no way to know which half of the war they are in.
//
// This is the switch that puts the war's colours back on the models, and it is
// on. What it covers, and what it does not, is worth writing down, because
// redrawing a player and not what they are holding looks like a bug rather
// than like a decision:
//
//   covered   player bodies and ragdolls (C_TFPlayer::GetSkin), world weapon
//             models (CTFWeaponBase::GetSkin), cosmetics (CTFWearable and
//             CEconWearable::GetSkin), the viewmodel and its attachments
//             (CTFViewModel, C_ViewmodelAttachmentModel), and buildings and
//             the ammo they drop (tf_obj.cpp, c_obj_sentrygun.cpp).
//
//   not       particle effects that carry a team in their name — rockets,
//             stickies, medic beams, crit and burning effects, roughly a
//             hundred and ninety call sites — and everything that is UI or
//             map rather than a model: the HUD, the scoreboard, name
//             colours, spawn room signs, respawn visualizers, the cart.
//
// So a player is red while the screen around them says BLU, and their rocket
// trail is still the team's colour. That is the price, and it is paid for a
// reason: the question the colours have to answer, for a player, is "which
// half of this war am I in".
//
// That is a real contradiction and it is the better of the two available
// ones. A player spends the battle looking at other players, not at the
// scoreboard, and the question the colours have to answer for them is "which
// half of this war am I in" — which the war's colours answer and the team's
// colours actively lie about on any map where one side always defends. The UI
// disagreeing is something to read past once; being told all evening that you
// are BLU when you are fighting for RED is something to be confused by
// continuously.
//
// It applies only to battles where the two disagree — a symmetric map, or a
// directional one the attacking side already wears the right colours on,
// changes nothing. greyline_uniform_by_war_side 0 turns it off entirely.
//
// What it never does is colour players individually by allegiance. Two people
// on the same team must always look the same as each other: in a shooter the
// colour of a body is the answer to "do I shoot this", and that answer cannot
// be allowed to depend on a campaign. The swap is per-team and total.
//
//=========================================================================//

#ifndef GREYLINE_UNIFORM_H
#define GREYLINE_UNIFORM_H
#ifdef _WIN32
#pragma once
#endif

namespace greyline
{

// True when this battle is being fought in swapped colours and the switch to
// show it is on.
bool UniformSwapped();

// The team whose colours iTeam should be drawn in. Identity unless the battle
// is swapped, in which case RED and BLU trade — every player alike.
int DisplayTeam( int iTeam );

#ifdef GAME_DLL
// Works out whether this battle's roster has the sides in each other's
// colours and publishes it to clients. Called whenever the roster changes.
void RecomputeUniformSwap();
#endif

} // namespace greyline

#endif // GREYLINE_UNIFORM_H
