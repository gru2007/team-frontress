//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The game's side of the Team Frontress demo.
//
//          The demo is a web page -- resource/html/frontress, opened in place
//          of the main menu -- and the page owns the whole campaign: sides,
//          sectors, operations, what a victory changes. The game does only
//          what a page cannot:
//
//            frontress_demo_deploy <ticket> <map> <red|blue> <players> <class> <swap>
//                start a local battle with bots, and put the player in it on
//                the page's game team, as its class; swap 1 is a RED offensive
//                on a BLU-attacks map, drawn in war colours through
//                greyline_uniform_swap (src/game/shared/greyline);
//            notice the round that decides it, publish the winner against
//                the ticket on GET /v1/demo/battle, and send the player back
//                to the menu, where the page reads it and moves the war.
//
//          The battle is an ordinary listen server; nothing here touches
//          game rules.
//
//=============================================================================//

#ifndef TF_FRONTRESS_DEMO_H
#define TF_FRONTRESS_DEMO_H
#ifdef _WIN32
#pragma once
#endif

// True when the main menu is the demo page rather than the stock menu.
// tf_frontress_demo 1 (the default on this branch), or -frontressdemo;
// -nofrontressdemo turns it off for a launch.
bool TFFrontressDemoMenu();

// The page the main menu's web panel opens.
const char *TFFrontressMenuPage();

#endif // TF_FRONTRESS_DEMO_H
