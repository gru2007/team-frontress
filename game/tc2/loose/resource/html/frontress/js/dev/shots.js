// Store-page screenshots of the menu, in exact, repeatable states.
//
//   index.html?shot=<name>&faction=RED|BLU&lang=english|russian
//
// Every state is reached by playing battles through the real rules
// (campaign.js), not by hand-writing a save, so a screenshot can never show
// something the demo cannot actually get into. Browser mode only: the page
// neither reads nor writes the player's campaign while a shot is set up.
// tools/shots/ui-shots.mjs walks the whole list.

import * as C from '../game/campaign.js';

const MVP = { score: 34, kills: 14, deaths: 3, damage: 4200, healing: 0, teamRank: 1 };
const AVERAGE = { score: 12, kills: 5, deaths: 6, damage: 1500, healing: 0, teamRank: 3 };

// Fight the battles the coordinator would pick; `results` is 'W', 'L' or 'S',
// and a lower-case letter is the same result with the player as best on the
// field.
function play( faction, results, { zones = {}, cls = 'soldier' } = {} ) {
	let s = C.chooseFaction( C.initialState(), faction );
	let n = 0;
	for ( const r of results ) {
		const plan = C.recommend( s );
		s = C.startBattle( s, { ...plan, zone: zones[ plan.stage ] || plan.zone, cls }, `shot${ n++ }` );
		const R = r.toUpperCase();
		const winner = R === 'W' ? faction : R === 'L' ? C.enemyOf( faction ) : null;
		s = C.resolveBattle( s, winner, { stats: r === R ? AVERAGE : MVP } );
	}
	return C.produce( s, d => { d.lastSeen = Date.now(); d.prefs.cls = cls; } );
}

// A battle that is being launched right now.
function launching( s, zone ) {
	const plan = C.recommend( s );
	return C.startBattle( s, { ...plan, zone: zone || plan.zone, cls: s.prefs.cls }, 'shotlive' );
}

export const SHOTS = {
	title:        f => ( { state: play( f, 'WW' ), ui: { screen: 'title' } } ),
	faction:      () => ( { state: C.initialState(), ui: { screen: 'faction' } } ),
	contract:     f => ( { state: C.initialState(), ui: { screen: 'faction', overlay: { type: 'contract', faction: f } } } ),
	war:          f => ( { state: play( f, 'W' ), ui: { screen: 'war' } } ),
	dossier:      f => ( { state: play( f, 'WLW' ), ui: { screen: 'war', selected: 'junction', zone: { 'junction:2': 'spur' } } } ),
	'front-moved': f => ( { state: play( f, 'WWWW' ), ui: { screen: 'war', selected: 'hydro' } } ),
	coordinator:  f => {
		const s = launching( play( f, 'W' ) );
		return { state: s, ui: { screen: 'war', overlay: { type: 'deploy', plan: s.pending, phase: 'routing' } } };
	},
	'debrief-win':     f => ( { state: play( f, 'WWw' ), ui: { screen: 'war', overlay: { type: 'debrief' } } } ),
	'debrief-saved':   f => ( { state: play( f, 'Wl' ), ui: { screen: 'war', overlay: { type: 'debrief' } } } ),
	threat:       f => ( { state: play( f, 'WwW' ), ui: { screen: 'war' } } ),
	defense:      f => ( { state: play( f, 'WwW' ), ui: { screen: 'war', selected: 'sawmill' } } ),
	support:      f => ( { state: play( f, 'Ww' ), ui: { screen: 'war', selected: 'junction', asset: 'intel' } } ),
	'debrief-advance': f => ( { state: play( f, 'W' ), ui: { screen: 'war', overlay: { type: 'debrief' } } } ),
	'debrief-loss':    f => ( { state: play( f, 'WWL' ), ui: { screen: 'war', overlay: { type: 'debrief' } } } ),
	journal:      f => ( { state: play( f, 'WwLWWwW' ), ui: { screen: 'war', tab: 'journal' } } ),
	briefing:     f => ( { state: play( f, 'W' ), ui: { screen: 'war', tab: 'briefing', briefPlayers: 24 } } ),
	ingame:       f => ( { state: launching( play( f, 'W' ) ), ui: { screen: 'war', inGame: true } } ),
	final:        f => ( { state: play( f, 'WWWWWWWWW' ), ui: { screen: 'war', overlay: { type: 'final' } } } ),
};

export function setUpShot( name, faction ) {
	const make = SHOTS[ name ];
	if ( !make )
		return null;
	const { state, ui } = make( faction === 'BLU' ? 'BLU' : 'RED' );
	// The debrief belongs on screen only where the shot asks for it.
	const s = ui.overlay?.type === 'debrief' ? state : C.produce( state, d => { d.debrief = null; } );
	return { state: s, ui };
}
