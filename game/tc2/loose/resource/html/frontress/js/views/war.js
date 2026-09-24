// The war room: the theater map with the war around it. This is the screen
// the player lives on between battles.

import { h } from '../core/dom.js';
import { t } from '../core/i18n.js';
import * as C from '../game/campaign.js';
import { buildTheater, theaterKey } from '../ui/theater.js';
import { sectorName, codename, stageName, modeName, logVars } from '../ui/names.js';
import { logo, emblem, stageTrack, momentum, chevrons } from './parts.js';
import { renderDossier } from './dossier.js';
import { renderJournal } from './journal.js';
import { renderBriefing } from './briefing.js';

// The theater is expensive to build and holds hover state: keep it across
// renders for as long as the war it shows has not changed.
let theater = null, theaterFor = null;

export function renderWar( ctx ) {
	const { state: s, ui, act } = ctx;

	const key = theaterKey( s ) + ( document.documentElement.lang );
	if ( key !== theaterFor ) {
		theater = buildTheater( s, { onSelect: id => act.select( id ) } );
		theaterFor = key;
	}
	theater.select( ui.selected );

	let body;
	if ( ui.tab === 'journal' )
		body = renderJournal( ctx );
	else if ( ui.tab === 'briefing' )
		body = renderBriefing( ctx );
	else
		body = h( 'div.war-body', null,
			sidebar( ctx ),
			h( 'main.theater-wrap', null,
				theater.el,
				legend( s ),
				deployDock( ctx ),
			),
			ui.selected ? renderDossier( ctx, ui.selected ) : null,
		);

	return h( 'div.screen.war-screen', { class: ui.selected && ui.tab === 'map' ? 'has-dossier' : '' },
		topbar( ctx ),
		body,
	);
}

//-----------------------------------------------------------------------------
function topbar( ctx ) {
	const { state: s, ui, act } = ctx;
	const share = C.controlShare( s );
	const tab = ( id, label ) => h( 'button', { class: `tab ${ ui.tab === id ? 'active' : '' }`, onclick: () => act.tab( id ) }, label );

	return h( 'header.topbar', null,
		h( 'button.brand', { onclick: () => ctx.set( { screen: 'title', selected: null } ), title: t( 'nav.menu' ) },
			logo( 'logo-small' ) ),
		h( 'nav.tabs', null,
			tab( 'map', t( 'nav.map' ) ),
			tab( 'journal', t( 'nav.journal' ) ),
			tab( 'briefing', t( 'nav.briefing' ) ),
		),
		h( 'div.tug', { title: t( 'hud.control' ) },
			h( 'div.tug-label', null, t( 'hud.control' ) ),
			h( 'div.tug-bar', null,
				h( 'div.tug-ally', { style: { width: `${ share * 100 }%` } }, h( 'span', null, `${ s.faction } ${ Math.round( share * 100 ) }%` ) ),
				h( 'div.tug-enemy', null, h( 'span', null, `${ Math.round( ( 1 - share ) * 100 ) }% ${ C.enemyOf( s.faction ) }` ) ),
			),
		),
		h( 'div.day-chip', null, h( 'span', null, t( 'hud.day' ) ), h( 'strong', null, String( s.day ) ) ),
		h( 'div.faction-chip', null, emblem( s.faction ), h( 'span', null, s.faction ) ),
		h( 'button.icon-btn', { onclick: () => act.overlay( 'settings' ), title: t( 'title.settings' ) }, gearIcon() ),
	);
}

function gearIcon() {
	return h( 'span.gear', { html: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 8.5a3.5 3.5 0 1 0 0 7 3.5 3.5 0 0 0 0-7Zm8.6 5.1-1.9-.4a7 7 0 0 1-.6 1.5l1.1 1.6-1.9 1.9-1.6-1.1c-.5.3-1 .5-1.5.6l-.4 1.9h-2.7l-.4-1.9a7 7 0 0 1-1.5-.6l-1.6 1.1-1.9-1.9 1.1-1.6a7 7 0 0 1-.6-1.5l-1.9-.4v-2.7l1.9-.4c.1-.5.3-1 .6-1.5L5.3 6.6l1.9-1.9 1.6 1.1c.5-.3 1-.5 1.5-.6l.4-1.9h2.7l.4 1.9c.5.1 1 .3 1.5.6l1.6-1.1 1.9 1.9-1.1 1.6c.3.5.5 1 .6 1.5l1.9.4v2.7Z"/></svg>' } );
}

//-----------------------------------------------------------------------------
function sidebar( ctx ) {
	const { state: s, act } = ctx;
	const op = s.op;

	const opCard = s.finished
		? h( 'section.card.op-card.hoverable', { onclick: () => act.overlay( 'final' ) },
			h( 'div.card-kicker', null, t( 'app.war' ) ),
			h( 'h2.op-name', null, t( 'final.title' ) ),
			h( 'p', null, t( 'final.stats', { battles: s.stats.battles, wins: s.stats.wins, captured: s.stats.captured, days: s.day } ) ) )
		: op
		? h( 'section.card.op-card.hoverable', { onclick: () => act.select( op.target ) },
			h( 'div.card-kicker', null, t( 'status.operation' ) ),
			h( 'h2.op-name', null, t( 'hud.op', { codename: codename( op.target ) } ) ),
			h( 'div.op-target', null, t( 'hud.opTarget', { target: sectorName( s, op.target ) } ) ),
			stageTrack( op.stage, { modes: C.operationFor( op.target ).map( ( zs, i ) => C.zoneFor( s, op.target, i + 1, ctx.ui.zone[ `${ op.target }:${ i + 1 }` ] ).mode ), compact: false } ),
			momentum( op.momentum ),
		)
		: h( 'section.card.op-card.empty', null,
			h( 'div.card-kicker', null, t( 'hud.noOp' ) ),
			h( 'p', null, t( 'hud.noOpBody' ) ),
			h( 'div.target-list', null,
				C.frontTargets( s ).map( id => h( 'button.target-chip', { onclick: () => act.select( id ) },
					h( 'span.chip-dot' ), sectorName( s, id ),
					s.pressure[ id ] < 0 ? h( 'span.chip-tag', null, '▲' ) : null ) ) ),
		);

	const stats = h( 'section.card.stats-card', null,
		stat( t( 'hud.battles' ), s.stats.battles ),
		stat( t( 'hud.wins' ), s.stats.wins, 'good' ),
		stat( t( 'hud.losses' ), s.stats.losses, 'bad' ),
		stat( t( 'hud.captured' ), s.stats.captured ),
	);

	const feed = s.log.slice( -7 ).reverse();
	const dispatches = h( 'section.card.feed-card', null,
		h( 'div.card-kicker', null, t( 'hud.dispatches' ) ),
		h( 'ol.feed', { 'data-scroll': 'feed' },
			feed.map( e => h( 'li', { class: `feed-item tone-${ e.tone }`, onclick: e.node ? () => act.select( e.node ) : null },
				h( 'span.feed-day', null, `${ t( 'hud.day' ) } ${ e.day }` ),
				h( 'span.feed-text', null, t( e.key, logVars( s, e ) ) ) ) ) ),
		h( 'button.link', { onclick: () => act.tab( 'journal' ) }, t( 'nav.journal' ) + ' →' ),
	);

	return h( 'aside.sidebar', null, opCard, stats, dispatches );
}

const stat = ( label, value, cls = '' ) =>
	h( 'div', { class: `stat ${ cls }` }, h( 'strong', null, String( value ) ), h( 'span', null, label ) );

function legend( s ) {
	return h( 'div.map-legend', null,
		h( 'span.lg.ally', null, h( 'i' ), s.faction ),
		h( 'span.lg.enemy', null, h( 'i' ), C.enemyOf( s.faction ) ),
		h( 'span.lg.front', null, h( 'i' ), t( 'hud.frontTargets' ) ),
		h( 'span.lg.target', null, h( 'i' ), t( 'status.target' ).split( '·' )[ 0 ].trim() ),
	);
}

//-----------------------------------------------------------------------------
// The big button. It always says what it is going to do.
function deployDock( ctx ) {
	const { state: s, act } = ctx;

	if ( s.pending ) {
		return h( 'div.deploy-dock', null,
			h( 'button.btn-deploy.busy', { onclick: () => act.overlay( 'pending' ) },
				h( 'span.deploy-word', null, t( 'deploy.pending' ) ) ) );
	}

	if ( s.finished ) {
		return h( 'div.deploy-dock', null,
			h( 'button.btn-deploy', { onclick: () => act.overlay( 'final' ) }, h( 'span.deploy-word', null, t( 'final.title' ) ) ) );
	}

	const r = C.recommend( s );
	if ( !r ) {
		return h( 'div.deploy-dock', null, h( 'button.btn-deploy', { disabled: true }, h( 'span.deploy-word', null, t( 'deploy.none' ) ) ) );
	}

	const zone = C.zoneFor( s, r.target, r.stage, ctx.ui.zone[ `${ r.target }:${ r.stage }` ] || r.zone );
	return h( 'div.deploy-dock', null,
		h( 'button.btn-deploy', { onclick: act.autoDeploy },
			chevrons(),
			h( 'span.deploy-word', null, t( 'deploy.button' ) ),
		),
		h( 'div.deploy-sub', null, t( 'deploy.auto', {
			target: sectorName( s, r.target ), stage: stageName( r.stage ), mode: modeName( zone.mode ),
		} ) ),
	);
}
