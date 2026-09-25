// The sector dossier: everything about one place on the map, and -- when a
// battle can be fought there -- the whole deployment: where to land, under
// which conditions, with what support, as whom, and what the battle decides.

import { h } from '../core/dom.js';
import { t } from '../core/i18n.js';
import * as C from '../game/campaign.js';
import { CLASSES, ASSETS, MODIFIERS } from '../game/scenario.js';
import { buildSectorMap } from '../ui/sector.js';
import { icon, modeIcon } from '../ui/glyphs.js';
import { sectorName, codename, stageName, modeName, zoneName, kindName, factionOf, mapTitle } from '../ui/names.js';
import { stageTrack, momentum, closeButton, chevrons, conditionChip } from './parts.js';

export function renderDossier( ctx, id ) {
	const { state: s, act } = ctx;
	const n = C.node( id );
	const status = C.sectorStatus( s, id );
	const owner = s.owners[ id ];

	const head = h( 'header.dossier-head', { class: `${ owner } ${ status === 'threat' ? 'threat' : '' }` },
		h( 'div.dossier-icon', null, icon( n.kind ) ),
		h( 'div.dossier-title', null,
			h( 'div.dossier-kind', null, kindName( n.kind ) ),
			h( 'h2', null, sectorName( s, id ) ),
			h( 'div', { class: `dossier-status status-${ status }` }, statusLine( s, id, status ) ),
		),
		closeButton( () => act.select( null ) ),
	);

	let body;
	if ( status === 'threat' )
		body = defense( ctx, id );
	else if ( status === 'target' || status === 'operation' )
		body = attack( ctx, id );
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
	if ( status === 'threat' )
		return t( 'status.threat', { n: s.threat.left } );
	return t( `status.${ status }` );
}

//-----------------------------------------------------------------------------
// Attacking an enemy sector.
//-----------------------------------------------------------------------------
function attack( ctx, id ) {
	const { state: s, ui, act } = ctx;
	const inOp = s.op?.target === id;
	const stage = inOp ? s.op.stage : 1;
	const info = C.stageInfo( stage );
	const zones = C.zonesFor( s, id, stage );
	const chosen = ui.zone[ `${ id }:${ stage }` ] || ( inOp ? s.op.zone : null );
	const zone = zones.find( z => z.id === chosen ) || zones[ 0 ];
	const asset = C.canAfford( s, ui.asset ) ? ui.asset : null;
	const plan = C.planBattle( s, { kind: 'attack', target: id, stage, zone: zone.id, asset } );
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
				h( 'span.meta', null, `${ 1 + plan.roster.allies } ${ t( 'common.vs' ) } ${ plan.roster.enemies }` ),
				h( 'span.meta', null, t( 'stage.minutes', { n: info.minutes } ) ) ),
		),

		conditions( plan ),
		zonePicker( ctx, zones, zone, stage, z => act.pickZone( id, stage, z ) ),
		support( ctx, plan ),
		classPicker( ctx, cls ),

		h( 'section.dossier-block.consequences', null,
			h( 'div.block-title', null, t( 'cons.heading' ) ),
			h( 'ul', null,
				h( 'li.good', null, winLine( s, id, stage ) ),
				h( 'li.bad', null, lossLine( s, stage, inOp ? s.op.momentum : C.MAX_MOMENTUM ) ),
				s.op && !inOp ? h( 'li.warn', null, t( 'cons.switch', { codename: codename( s.op.target ) } ) ) : null,
				s.threat ? h( 'li.warn', null, t( 'defense.ignore', { n: s.threat.left, target: sectorName( s, s.threat.node ) } ) ) : null,
			),
		),

		h( 'div.dossier-actions', null,
			h( 'button.btn.btn-primary.btn-deploy-here', {
				disabled: !!s.pending,
				onclick: () => act.deploy( { kind: 'attack', target: id, stage, zone: zone.id, cls, asset } ),
			}, chevrons(), t( 'deploy.here' ) ),
		),
	);
}

//-----------------------------------------------------------------------------
// Holding an ally sector against a counter-attack.
//-----------------------------------------------------------------------------
function defense( ctx, id ) {
	const { state: s, ui, act } = ctx;
	const zones = C.defenseZones( s, id );
	const zone = zones.find( z => z.id === ui.zone[ `def:${ id }` ] ) || zones[ 0 ];
	const asset = C.canAfford( s, ui.asset ) ? ui.asset : null;
	const plan = C.planBattle( s, { kind: 'defense', target: id, zone: zone.id, asset } );
	const cls = s.prefs.cls || 'any';
	const attacker = factionOf( s, 'enemy' );

	return h( 'div.dossier-body', null,
		h( 'section.dossier-block.threat-brief', null,
			h( 'div.stage-kicker', null, t( 'defense.kicker' ) ),
			h( 'h3', null, t( 'defense.title' ) ),
			h( 'p', null, t( s.threat.left <= 1 ? 'threat.bodyLast' : 'threat.body',
				{ faction: attacker, target: sectorName( s, id ), n: s.threat.left } ) ),
			h( 'p', null, t( 'defense.desc' ) ),
			h( 'div.stage-meta', null,
				h( 'span.meta', null, `${ 1 + plan.roster.allies } ${ t( 'common.vs' ) } ${ plan.roster.enemies }` ) ),
		),

		conditions( plan ),
		zonePicker( ctx, zones, zone, 2, z => act.pickZone( 'def', id, z ) ),
		support( ctx, plan ),
		classPicker( ctx, cls ),

		h( 'section.dossier-block.consequences', null,
			h( 'div.block-title', null, t( 'cons.heading' ) ),
			h( 'ul', null,
				h( 'li.good', null, t( 'defense.win' ) ),
				h( 'li.bad', null, t( 'defense.loss', { target: sectorName( s, id ) } ) ),
				h( 'li.warn', null, t( 'defense.ignore', { n: s.threat.left, target: sectorName( s, id ) } ) ),
			),
		),

		h( 'div.dossier-actions', null,
			h( 'button.btn.btn-primary.btn-deploy-here.btn-defend', {
				disabled: !!s.pending,
				onclick: () => act.deploy( { kind: 'defense', target: id, zone: zone.id, cls, asset } ),
			}, chevrons(), t( 'deploy.defend', { target: sectorName( s, id ) } ) ),
		),
	);
}

//-----------------------------------------------------------------------------
// Pieces
//-----------------------------------------------------------------------------
function conditions( plan ) {
	return h( 'section.dossier-block.conditions', null,
		h( 'div.block-title', null, t( 'cond.heading' ) ),
		plan.mods.length
			? h( 'div.cond-list', null, plan.mods.map( id => conditionChip( id, { detail: true } ) ) )
			: h( 'p.cond-none', null, t( 'cond.none' ) ),
		plan.cancelled ? h( 'p.cond-cancelled', null, t( 'cond.cancelled', { name: t( `mod.${ plan.cancelled }` ) } ) ) : null,
		plan.reward ? h( 'p.cond-reward', null, t( 'cond.reward', { n: plan.reward } ) ) : null,
	);
}

function zonePicker( ctx, zones, zone, stage, pick ) {
	return h( 'section.dossier-block', null,
		h( 'div.block-title', null, t( 'zone.heading' ) ),
		buildSectorMap( zones, zone.id, stage, pick ),
		h( 'div.zone-cards', null,
			zones.map( ( z, i ) => h( 'button', {
				class: `zone-card ${ z.id === zone.id ? 'active' : '' }`,
				onclick: () => pick( z.id ),
			},
				h( 'span.zone-letter', null, String.fromCharCode( 65 + i ) ),
				h( 'span.zone-mode', null, modeIcon( z.mode ) ),
				h( 'span.zone-text', null,
					h( 'strong', null, zoneName( z.id ) ),
					h( 'span', null, `${ modeName( z.mode ) } · ${ mapTitle( z.map ) }` ) ),
			) ) ),
		h( 'p.zone-goal', null, t( `mode.${ zone.mode }.goal` ) ),
	);
}

function support( ctx, plan ) {
	const { state: s, ui, act } = ctx;
	return h( 'section.dossier-block.support', null,
		h( 'div.block-title.support-title', null, t( 'asset.heading' ),
			h( 'span.supply-chip', { title: t( 'hud.supplyHint' ) }, supplyIcon(), String( s.supply ) ) ),
		h( 'div.asset-grid', null,
			Object.entries( ASSETS ).map( ( [ id, a ] ) => {
				const affordable = s.supply >= a.cost;
				// Scouting with nothing to scout is not an option.
				const pointless = a.cancels && !plan.mods.some( m => MODIFIERS[ m ].tone === a.cancels ) && plan.cancelled == null;
				return h( 'button', {
					class: `asset-card ${ ui.asset === id && affordable ? 'active' : '' } ${ id === 'gamble' ? 'risk' : '' }`,
					disabled: !affordable || pointless,
					title: affordable ? t( `asset.${ id }.desc` ) : t( 'asset.cantAfford' ),
					onclick: () => act.pickAsset( id ),
				},
					h( 'strong', null, t( `asset.${ id }` ) ),
					h( 'span.asset-desc', null, t( `asset.${ id }.desc` ) ),
					h( 'span.asset-cost', null, a.cost ? t( 'asset.cost', { n: a.cost } ) : t( 'asset.free' ) ),
				);
			} ) ),
	);
}

function classPicker( ctx, cls ) {
	const { act } = ctx;
	return h( 'section.dossier-block', null,
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
	);
}

export function supplyIcon() {
	return h( 'span.supply-icon', { html: '<svg viewBox="-12 -12 24 24" aria-hidden="true"><path d="M-9 -3 L0 -8 L9 -3 L9 7 L0 11 L-9 7 Z M-9 -3 L0 1 L9 -3 M0 1 L0 11" /></svg>' } );
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
