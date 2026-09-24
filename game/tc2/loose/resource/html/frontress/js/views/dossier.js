// The sector dossier: everything about one place on the map, and -- when it
// can be attacked -- the whole deployment: where to land, what to do there,
// and what the battle will decide.

import { h } from '../core/dom.js';
import { t } from '../core/i18n.js';
import * as C from '../game/campaign.js';
import { CLASSES } from '../game/scenario.js';
import { buildSectorMap } from '../ui/sector.js';
import { icon, modeIcon } from '../ui/glyphs.js';
import { sectorName, codename, stageName, modeName, zoneName, kindName, factionOf, mapTitle } from '../ui/names.js';
import { stageTrack, momentum, closeButton, chevrons } from './parts.js';

export function renderDossier( ctx, id ) {
	const { state: s, act } = ctx;
	const n = C.node( id );
	const status = C.sectorStatus( s, id );
	const owner = s.owners[ id ];

	const head = h( 'header.dossier-head', { class: owner },
		h( 'div.dossier-icon', null, icon( n.kind ) ),
		h( 'div.dossier-title', null,
			h( 'div.dossier-kind', null, kindName( n.kind ) ),
			h( 'h2', null, sectorName( s, id ) ),
			h( 'div', { class: `dossier-status status-${ status }` }, statusLine( s, id, status ) ),
		),
		closeButton( () => act.select( null ) ),
	);

	let body;
	if ( status === 'target' || status === 'operation' )
		body = deployment( ctx, id );
	else if ( status === 'deep' )
		body = h( 'div.dossier-note', null,
			h( 'p', null, t( 'status.deepBody', {
				via: C.neighbours( id ).filter( m => C.frontTargets( s ).includes( m ) ).map( m => sectorName( s, m ) ).join( ', ' ) || '—',
			} ) ) );
	else
		body = h( 'div.dossier-note', null,
			h( 'p', null, t( 'status.allyBody' ) ),
			s.pressure[ id ] > 0 ? h( 'p.warn', null, t( 'status.contested' ) ) : null );

	return h( 'aside.dossier', { 'data-scroll': `dossier-${ id }` }, head, body );
}

function statusLine( s, id, status ) {
	if ( status === 'frontline' || status === 'secure' )
		return t( `status.${ status }`, { faction: factionOf( s, s.owners[ id ] ) } );
	return t( `status.${ status }` );
}

//-----------------------------------------------------------------------------
function deployment( ctx, id ) {
	const { state: s, ui, act } = ctx;
	const inOp = s.op?.target === id;
	const stage = inOp ? s.op.stage : 1;
	const info = C.stageInfo( stage );
	const zones = C.zonesFor( s, id, stage );
	const chosen = ui.zone[ `${ id }:${ stage }` ] || ( inOp ? s.op.zone : null );
	const zone = zones.find( z => z.id === chosen ) || zones[ 0 ];
	const cls = s.prefs.cls || 'any';

	const modes = [ 1, 2, 3 ].map( st => ( st === stage ? zone : C.zonesFor( s, id, st )[ 0 ] ).mode );

	return h( 'div.dossier-body', null,
		h( 'section.dossier-block', null,
			h( 'div.block-title', null, t( 'hud.op', { codename: codename( id ) } ) ),
			stageTrack( stage, { modes } ),
			inOp ? momentum( s.op.momentum ) : null,
		),

		h( 'section.dossier-block.stage-brief', null,
			h( 'div.stage-kicker', null, t( 'stage.n', { n: stage } ) ),
			h( 'h3', null, stageName( stage ) ),
			h( 'p', null, t( `stage.${ info.id }.desc` ) ),
			h( 'div.stage-meta', null,
				h( 'span.meta', null, t( 'stage.size', { size: info.size, bots: info.players - 1 } ) ),
				h( 'span.meta', null, t( 'stage.minutes', { n: info.minutes } ) ) ),
		),

		h( 'section.dossier-block', null,
			h( 'div.block-title', null, t( 'zone.heading' ) ),
			buildSectorMap( zones, zone.id, stage, z => act.pickZone( id, stage, z ) ),
			h( 'div.zone-cards', null,
				zones.map( ( z, i ) => h( 'button', {
					class: `zone-card ${ z.id === zone.id ? 'active' : '' }`,
					onclick: () => act.pickZone( id, stage, z.id ),
				},
					h( 'span.zone-letter', null, String.fromCharCode( 65 + i ) ),
					h( 'span.zone-mode', null, modeIcon( z.mode ) ),
					h( 'span.zone-text', null,
						h( 'strong', null, zoneName( z.id ) ),
						h( 'span', null, `${ modeName( z.mode ) } · ${ mapTitle( z.map ) }` ) ),
				) ) ),
			h( 'p.zone-goal', null, t( `mode.${ zone.mode }.goal` ) ),
		),

		h( 'section.dossier-block', null,
			h( 'div.block-title', null, t( 'class.heading' ) ),
			h( 'div.class-grid', null,
				h( 'button', { class: `class-btn any ${ cls === 'any' ? 'active' : '' }`, onclick: () => act.pickClass( 'any' ), title: t( 'class.any' ) },
					h( 'span.any-mark', null, '?' ) ),
				CLASSES.map( c => h( 'button', {
					class: `class-btn ${ cls === c.id ? 'active' : '' }`, onclick: () => act.pickClass( c.id ), title: t( `class.${ c.id }` ),
				}, h( 'img', { src: `../img/icons/${ c.icon }.png`, alt: t( `class.${ c.id }` ) } ) ) ),
			),
			h( 'div.class-name', null, cls === 'any' ? t( 'class.any' ) :
				`${ t( `class.${ cls }` ) } · ${ t( `role.${ CLASSES.find( c => c.id === cls ).role }` ) }` ),
		),

		h( 'section.dossier-block.consequences', null,
			h( 'div.block-title', null, t( 'cons.heading' ) ),
			h( 'ul', null,
				h( 'li.good', null, winLine( s, id, stage ) ),
				h( 'li.bad', null, lossLine( s, stage, inOp ? s.op.momentum : C.MAX_MOMENTUM ) ),
				s.op && !inOp ? h( 'li.warn', null, t( 'cons.switch', { codename: codename( s.op.target ) } ) ) : null,
			),
		),

		h( 'div.dossier-actions', null,
			h( 'button.btn.btn-primary.btn-deploy-here', {
				disabled: !!s.pending,
				onclick: () => act.deploy( { target: id, stage, zone: zone.id, cls } ),
			}, chevrons(), t( 'deploy.here' ) ),
		),
	);
}

function winLine( s, id, stage ) {
	if ( stage < 3 ) return t( 'cons.winNext', { stage: stageName( stage + 1 ) } );
	if ( C.node( id ).kind === 'hq' ) return t( 'cons.winWar' );
	return t( 'cons.winCapture', { target: sectorName( s, id ) } );
}

function lossLine( s, stage, momentumLeft ) {
	if ( momentumLeft <= 1 ) return t( 'cons.collapse' );
	if ( stage > 1 ) return t( 'cons.loss', { stage: stageName( stage - 1 ) } );
	return t( 'cons.lossHold' );
}
