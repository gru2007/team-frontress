// What the menu shows while a battle is running and the player pressed Esc:
// where they are in the war, and the ways out.

import { h } from '../core/dom.js';
import { t } from '../core/i18n.js';
import * as C from '../game/campaign.js';
import { sectorName, codename, stageName, modeName, mapTitle } from '../ui/names.js';
import { emblem, stageTrack, momentum, logo } from './parts.js';
import { modeIcon } from '../ui/glyphs.js';

export function renderInGame( ctx ) {
	const { state: s, act } = ctx;
	const p = s.pending;

	const body = p
		? h( 'div.ingame-card', null,
			h( 'div.ingame-head', null,
				emblem( s.faction, 'ingame-emblem' ),
				h( 'div', null,
					h( 'div.paper-kicker', null, t( 'ingame.title' ) ),
					h( 'h2', null, sectorName( s, p.target ) ),
					h( 'div.ingame-sub', null, t( 'hud.op', { codename: codename( p.target ) } ) ) ) ),
			h( 'div.battle-card', null,
				h( 'div.bc-mode', null, modeIcon( p.mode ) ),
				h( 'div.bc-text', null,
					h( 'div.bc-map', null, mapTitle( p.map ) ),
					h( 'div.bc-sub', null, `${ stageName( p.stage ) } · ${ modeName( p.mode ) } · ${ C.stageInfo( p.stage ).size }` ) ) ),
			s.op ? stageTrack( s.op.stage ) : null,
			s.op ? momentum( s.op.momentum ) : null,
			p.swap ? h( 'p.ingame-note.swap-note', null, t( 'ingame.swapped', { team: p.team, faction: s.faction } ) ) : null,
			h( 'p.ingame-note', null, t( 'ingame.retreatBody' ) ),
		)
		: h( 'div.ingame-card', null,
			h( 'div.paper-kicker', null, t( 'ingame.title' ) ),
			h( 'p', null, t( 'ingame.practice' ) ) );

	return h( 'div.screen.ingame-screen', null,
		logo( 'logo-small' ),
		body,
		h( 'nav.ingame-menu', null,
			h( 'button.btn.btn-primary.btn-xl', { onclick: act.resume }, t( 'ingame.resume' ) ),
			h( 'button.btn', { onclick: act.options }, t( 'title.options' ) ),
			h( 'button.btn.btn-bad', { onclick: act.leaveBattle }, t( 'ingame.retreat' ) ),
		),
	);
}
