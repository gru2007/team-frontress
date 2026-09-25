// Pieces several screens share.

import { h, svg } from '../core/dom.js';
import { t } from '../core/i18n.js';
import { STAGES, MODIFIERS } from '../game/scenario.js';
import { MAX_MOMENTUM } from '../game/campaign.js';
import { modeIcon } from '../ui/glyphs.js';
import { stageName } from '../ui/names.js';

export function logo( cls = '' ) {
	return h( 'div', { class: `logo ${ cls }` },
		h( 'div.logo-team', null, 'TEAM' ),
		h( 'div.logo-main', null, 'FRONTRESS' ),
		h( 'div.logo-war', null, h( 'span.rule' ), t( 'app.war' ), h( 'span.rule' ) ),
	);
}

// Not the TF2 logos: a shield with the side's initial, in its colours.
export function emblem( faction, cls = '' ) {
	return svg( 'svg', { class: `emblem ${ faction } ${ cls }`, viewBox: '0 0 100 112', 'aria-hidden': 'true' },
		svg( 'path.em-shield', { d: 'M50 4 L92 18 L88 62 Q82 92 50 108 Q18 92 12 62 L8 18 Z' } ),
		svg( 'path.em-inner', { d: 'M50 14 L83 25 L80 60 Q75 84 50 97 Q25 84 20 60 L17 25 Z' } ),
		faction === 'RED'
			? svg( 'path.em-mark', { d: 'M33 30 L55 30 Q70 30 70 44 Q70 55 58 57 L72 80 L60 80 L47 58 L44 58 L44 80 L33 80 Z M44 39 L44 50 L54 50 Q59 50 59 44.5 Q59 39 54 39 Z' } )
			: svg( 'path.em-mark', { d: 'M32 30 L56 30 Q70 30 70 42 Q70 50 63 53 Q72 56 72 66 Q72 80 57 80 L32 80 Z M43 39 L43 50 L54 50 Q59 50 59 44.5 Q59 39 54 39 Z M43 58 L43 71 L56 71 Q61 71 61 64.5 Q61 58 56 58 Z' } ),
	);
}

// Breakthrough -> Advance -> Assault -> Captured, with where the attack is.
export function stageTrack( stage, { modes = [], compact = false } = {} ) {
	const steps = STAGES.map( ( st, i ) => {
		const n = i + 1;
		const state = n < stage ? 'done' : n === stage ? 'current' : 'todo';
		return h( 'div', { class: `step ${ state }` },
			h( 'div.step-node', null, modes[ i ] ? modeIcon( modes[ i ] ) : String( n ) ),
			compact ? null : h( 'div.step-label', null, stageName( n ) ),
			compact ? null : h( 'div.step-size', null, st.size ),
		);
	} );
	const capture = h( 'div', { class: `step final ${ stage >= 4 ? 'done' : 'todo' }` },
		h( 'div.step-node', null, svg( 'svg', { viewBox: '-12 -12 24 24', class: 'glyph' },
			svg( 'path', { d: 'M-5 9 L-5 -10 M-5 -10 L8 -6 L-5 -1 Z', class: 'g-fill' } ) ) ),
		compact ? null : h( 'div.step-label', null, t( 'stage.captured' ) ),
	);
	return h( 'div', { class: `stage-track ${ compact ? 'compact' : '' }`, style: { '--progress': Math.min( 3, Math.max( 0, stage - 1 ) ) / 3 } },
		h( 'div.track-line' ), steps, capture );
}

export function momentum( n ) {
	return h( 'div.momentum', { title: t( 'hud.momentumHint' ) },
		h( 'span.momentum-label', null, t( 'hud.momentum' ) ),
		Array.from( { length: MAX_MOMENTUM }, ( _, i ) => h( 'span', { class: `pip ${ i < n ? 'on' : '' }` } ) ) );
}

// '›››' drawn, because the TF2 fonts have no guillemets.
export function chevrons() {
	return svg( 'svg', { class: 'deploy-chevrons', viewBox: '0 0 30 16', 'aria-hidden': 'true' },
		svg( 'path', { d: 'M2 2 L8 8 L2 14 M11 2 L17 8 L11 14 M20 2 L26 8 L20 14' } ) );
}

export function stamp( text, cls = '' ) {
	return h( 'div', { class: `stamp ${ cls }` }, h( 'span', null, text ) );
}

export function closeButton( onclick ) {
	return h( 'button.icon-btn.close-x', { onclick, 'aria-label': t( 'common.close' ) }, '×' );
}

// One battle condition. `detail` adds its description (dossier); without it
// the chip is a compact label (coordinator, pause screen, debrief).
export function conditionChip( id, { detail = false } = {} ) {
	const tone = MODIFIERS[ id ]?.tone || 'odd';
	return h( 'div', { class: `cond cond-${ tone } ${ detail ? 'cond-detail' : '' }`, title: t( `mod.${ id }.desc` ) },
		h( 'span.cond-mark', null, tone === 'hard' ? '▲' : tone === 'good' ? '+' : '≈' ),
		h( 'span.cond-text', null,
			h( 'strong', null, t( `mod.${ id }` ) ),
			detail ? h( 'span', null, t( `mod.${ id }.desc` ) ) : null ) );
}

// A medal: a ribbon with its name.
export function medal( id ) {
	return h( 'div', { class: `medal medal-${ id }`, title: t( `medal.${ id }.desc` ) },
		h( 'span.medal-ribbon' ),
		h( 'span.medal-name', null, t( `medal.${ id }` ) ) );
}
