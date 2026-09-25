// Everything that sits on top of a screen: deployment, debrief, bulletins,
// settings, confirmations.

import { h } from '../core/dom.js';
import { t, LANGUAGES, language } from '../core/i18n.js';
import * as C from '../game/campaign.js';
import { sectorName, codename, stageName, modeName, zoneName, mapTitle, factionOf } from '../ui/names.js';
import { modeIcon } from '../ui/glyphs.js';
import { emblem, stageTrack, momentum, stamp, closeButton, logo, chevrons, conditionChip, medal } from './parts.js';

export function renderOverlay( ctx ) {
	const o = ctx.ui.overlay;
	const view = VIEWS[ o.type ];
	return view ? view( ctx, o ) : null;
}

const modal = ( cls, ...children ) => h( 'div', { class: `modal ${ cls }` }, ...children );

//-----------------------------------------------------------------------------
const VIEWS = {
	contract( ctx, o ) {
		const f = o.faction;
		return modal( `contract side-${ f }`,
			h( 'div.paper', null,
				h( 'div.paper-head', null,
					emblem( f, 'contract-emblem' ),
					h( 'div', null,
						h( 'div.paper-kicker', null, t( 'faction.contract' ) ),
						h( 'h2', null, t( `faction.${ f }.full` ) ) ) ),
				h( 'p.paper-body', null, t( 'faction.contractBody', { full: t( `faction.${ f }.full` ) } ) ),
				h( 'div.signature', null, h( 'span.sig-line' ), h( 'span.sig-x', null, '✕' ) ),
				stamp( f, `stamp-${ f }` ),
			),
			h( 'div.modal-actions', null,
				h( 'button.btn', { onclick: () => ctx.act.overlay( null ) }, t( 'faction.back' ) ),
				h( 'button.btn.btn-primary', { onclick: () => ctx.act.chooseFaction( f ) }, t( 'faction.sign' ) ),
			),
		);
	},

	welcome( ctx ) {
		const s = ctx.state;
		return modal( 'welcome',
			h( 'div.welcome-head', null, emblem( s.faction, 'welcome-emblem' ),
				h( 'div', null,
					h( 'div.paper-kicker', null, t( 'app.war' ) ),
					h( 'h2', null, t( 'log.enlisted', { faction: s.faction } ) ) ) ),
			h( 'ol.welcome-steps', null,
				h( 'li', null, h( 'strong', null, '1' ), h( 'span', null, t( 'brief.deploy.body' ) ) ),
				h( 'li', null, h( 'strong', null, '2' ), h( 'span', null, t( 'brief.op.body' ) ) ),
				h( 'li', null, h( 'strong', null, '3' ), h( 'span', null, t( 'brief.persist.body' ) ) ),
			),
			h( 'div.modal-actions', null,
				h( 'button.btn', { onclick: () => { ctx.act.overlay( null ); ctx.act.tab( 'briefing' ); } }, t( 'nav.briefing' ) ),
				h( 'button.btn.btn-primary', { onclick: () => ctx.act.overlay( null ) }, t( 'nav.map' ) ),
			),
		);
	},

	//-------------------------------------------------------------------------
	// DEPLOY: the coordinator's reasoning, then the loading hand-off.
	deploy( ctx, o ) {
		const s = ctx.state;
		const p = o.plan;
		const defense = p.kind === 'defense';
		const size = p.roster ? `${ 1 + p.roster.allies } ${ t( 'common.vs' ) } ${ p.roster.enemies }` : C.stageInfo( p.stage ).size;

		const lines = [
			defense
				? [ t( 'coord.threat' ), `${ sectorName( s, p.target ) } — ${ t( 'defense.title' ) }` ]
				: [ t( 'coord.front' ), `${ sectorName( s, p.target ) } — ${ t( 'hud.op', { codename: codename( p.target ) } ) }` ],
			defense ? null : [ t( 'coord.stage' ), `${ stageName( p.stage ) } · ${ t( 'stage.n', { n: p.stage } ) }` ],
			[ t( 'coord.map' ), `${ mapTitle( p.map ) } · ${ modeName( p.mode ) } · ${ zoneName( p.zone ) }` ],
			[ t( 'coord.conditions' ), p.mods?.length ? p.mods.map( id => t( `mod.${ id }` ) ).join( ' · ' ) : t( 'cond.none' ) ],
			p.asset ? [ t( 'coord.asset' ), t( `asset.${ p.asset }` ) ] : null,
			[ t( 'coord.side' ), p.swap ? t( 'coord.swapped', { faction: s.faction, team: p.team } ) : t( 'coord.yourSide', { faction: s.faction } ) ],
			[ t( 'coord.match' ), t( 'coord.newMatch', { size } ) ],
		].filter( Boolean );

		const card = h( 'div.battle-card', null,
			h( 'div.bc-mode', null, modeIcon( p.mode ) ),
			h( 'div.bc-text', null,
				h( 'div.bc-map', null, mapTitle( p.map ) ),
				h( 'div.bc-sub', null, `${ modeName( p.mode ) } · ${ size } · ${ p.map }` ) ),
			h( 'div.bc-side', null, emblem( s.faction ) ),
		);

		let tail;
		if ( o.phase === 'routing' ) {
			tail = h( 'div.coord-progress', null, h( 'span' ) );
		} else if ( o.phase === 'launching' ) {
			tail = h( 'div.launching', null,
				h( 'div.spinner' ),
				h( 'div', null,
					h( 'strong', null, t( 'coord.launching', { map: p.map } ) ),
					h( 'p', null, t( 'coord.launchingBody' ) ) ),
			);
		} else {
			tail = h( 'div.sim', null,
				h( 'div.sim-head', null, h( 'strong', null, t( 'sim.title' ) ), h( 'span', null, t( 'sim.body' ) ) ),
				h( 'div.sim-buttons', null,
					h( 'button.btn.btn-good', { onclick: () => ctx.act.simulate( 'ally' ) }, t( 'sim.win' ) ),
					h( 'button.btn.btn-good', { onclick: () => ctx.act.simulate( 'ally', true ) }, t( 'sim.winMvp' ) ),
					h( 'button.btn.btn-bad', { onclick: () => ctx.act.simulate( 'enemy' ) }, t( 'sim.loss' ) ),
					h( 'button.btn.btn-bad', { onclick: () => ctx.act.simulate( 'enemy', true ) }, t( 'sim.lossMvp' ) ),
					h( 'button.btn', { onclick: () => ctx.act.simulate( null ) }, t( 'sim.stalemate' ) ),
				) );
		}

		return modal( `deploy-modal phase-${ o.phase }`,
			h( 'div.coord-head', null,
				h( 'span.coord-light' ),
				h( 'span.coord-title', null, t( 'coord.title' ) ),
				h( 'span.coord-status', null, o.phase === 'routing' ? t( 'coord.routing' ) : '' ) ),
			h( 'ol.coord-lines', null, lines.map( ( [ k, v ], i ) =>
				h( 'li.coord-line', { style: { '--d': i } },
					h( 'span.coord-check', null, '✓' ),
					h( 'span.coord-key', null, k ),
					h( 'span.coord-val', null, v ) ) ) ),
			card,
			tail,
			o.phase !== 'launching' || ctx.bridge !== 'game'
				? h( 'div.modal-actions', null, h( 'button.btn.btn-quiet', { onclick: ctx.act.cancelDeploy }, t( 'coord.cancel' ) ) )
				: null,
		);
	},

	pending( ctx ) {
		const p = ctx.state.pending;
		if ( !p ) return null;
		return modal( 'pending-modal',
			h( 'h2', null, t( 'pending.title' ) ),
			h( 'p', null, t( 'pending.body', { target: sectorName( ctx.state, p.target ), map: p.map } ) ),
			h( 'div.modal-actions', null,
				h( 'button.btn', { onclick: ctx.act.abandon }, t( 'pending.abandon' ) ),
				ctx.bridge === 'game'
					? h( 'button.btn.btn-primary', { onclick: ctx.act.rejoin }, t( 'pending.rejoin' ) )
					: h( 'button.btn.btn-primary', { onclick: () => ctx.set( { overlay: { type: 'deploy', plan: p, phase: 'simulate' } } ) }, t( 'sim.title' ) ),
			),
		);
	},

	//-------------------------------------------------------------------------
	// After the battle: the answer to "what changed because of this victory?"
	debrief( ctx ) {
		const s = ctx.state;
		const d = s.debrief;
		if ( !d ) return null;

		const changes = [];
		const defense = d.kind === 'defense';
		if ( defense ) {
			if ( d.defended ) changes.push( [ 'good big', t( 'debrief.defended', { target: sectorName( s, d.target ) } ) ] );
			else changes.push( [ 'bad big', t( 'debrief.sectorLost', { target: sectorName( s, d.target ) } ) ] );
		} else if ( d.outcome === 'win' ) {
			if ( d.warWon ) changes.push( [ 'good big', t( 'debrief.warWon' ) ] );
			else if ( d.captured ) changes.push( [ 'good big', t( 'debrief.captured', { target: sectorName( s, d.target ) } ) ] );
			else changes.push( [ 'good', t( 'debrief.advanced', { from: stageName( d.stageBefore ), to: stageName( d.stageAfter ) } ) ] );
		} else {
			if ( d.collapsed ) changes.push( [ 'bad big', t( 'debrief.collapsed', { target: sectorName( s, d.target ) } ) ] );
			else if ( d.stageAfter < d.stageBefore ) changes.push( [ 'bad', t( 'debrief.pushedBack', { from: stageName( d.stageBefore ), to: stageName( d.stageAfter ) } ) ] );
			else changes.push( [ 'bad', t( 'debrief.held', { stage: stageName( d.stageAfter ) } ) ] );
			if ( !d.collapsed ) changes.push( [ d.saved ? 'good' : 'bad', d.saved ? t( 'debrief.saved' ) : t( 'debrief.momentum', { n: d.momentumAfter } ) ] );
			if ( d.outcome === 'stalemate' ) changes.push( [ 'note', t( 'debrief.stalemateNote' ) ] );
		}
		if ( d.momentumBonus ) changes.push( [ 'good', t( 'debrief.momentumBonus' ) ] );
		if ( d.threatLost ) changes.push( [ 'bad big', t( 'debrief.threatLost', { target: sectorName( s, d.threatLost ) } ) ] );
		if ( d.opCut ) changes.push( [ 'bad', t( 'debrief.opCut', { codename: codename( d.opCut ) } ) ] );

		// The player's own line: what they did, what it earned.
		const you = h( 'section.debrief-you', null,
			h( 'div.block-title', null, t( 'debrief.you' ) ),
			h( 'p.you-stats', null, d.stats ? t( 'debrief.statsLine', d.stats ) : t( 'debrief.noStats' ) ),
			d.medals?.length ? h( 'div.medal-row', null, d.medals.map( medal ) ) : null,
			h( 'div.you-gains', null,
				d.supplyGain ? h( 'span.gain.supply', null, t( 'debrief.supply', { n: d.supplyGain } ) ) : null,
				d.xpGain ? h( 'span.gain.xp', null, t( 'debrief.xp', { n: d.xpGain } ) ) : null,
				d.rankAfter && d.rankAfter !== d.rankBefore ? h( 'span.gain.rank', null, t( 'debrief.promoted', { rank: t( `rank.${ d.rankAfter }` ) } ) ) : null ),
		);
		const fought = d.mods?.length ? h( 'div.debrief-conds', null, d.mods.map( id => conditionChip( id ) ) ) : null;

		const world = d.world && d.world.id !== 'quiet_front'
			? h( 'section.meanwhile', { class: `tone-${ d.world.tone }` },
				h( 'div.block-title', null, t( 'debrief.meanwhile' ) ),
				h( 'p', null, t( `world.${ d.world.id }` ) ) )
			: null;

		const trackStage = d.captured ? 4 : Math.max( 1, d.stageAfter );
		const showTrack = !defense && !d.collapsed && !d.opCut;
		const title = t( `debrief.${ d.outcome }` );

		return modal( `debrief-modal outcome-${ d.outcome }`,
			h( 'div.debrief-hero', null,
				stamp( title.toUpperCase(), `stamp-${ d.outcome }` ),
				h( 'div.debrief-where', null,
					h( 'div.paper-kicker', null, defense ? t( 'defense.kicker' ) : t( 'hud.op', { codename: codename( d.target ) } ) ),
					h( 'h2', null, sectorName( s, d.target ) ),
					h( 'div.debrief-map', null, modeIcon( d.mode ), `${ modeName( d.mode ) } · ${ mapTitle( d.map ) }` ),
					fought ),
			),
			h( 'section.debrief-changes', null,
				h( 'div.block-title', null, t( 'debrief.question' ) ),
				h( 'ul', null, changes.map( ( [ cls, text ] ) => h( 'li', { class: cls }, text ) ) ),
				!showTrack ? null : h( 'div.debrief-track', { style: { '--from': Math.max( 0, Math.min( 3, d.stageBefore - 1 ) ) / 3 } },
					stageTrack( trackStage ),
					!d.captured && s.op ? momentum( s.op.momentum ) : null ),
			),
			you,
			world,
			h( 'div.debrief-stats', null,
				`${ t( 'hud.battles' ) } ${ s.stats.battles } · ${ t( 'hud.wins' ) } ${ s.stats.wins } · ${ t( 'hud.losses' ) } ${ s.stats.losses }` ),
			h( 'div.modal-actions', null,
				h( 'button.btn', { onclick: () => ctx.act.closeDebrief( false ) }, t( 'debrief.toMap' ) ),
				d.warWon ? null : h( 'button.btn.btn-primary', { onclick: () => ctx.act.closeDebrief( true ) },
					chevrons(), t( 'debrief.next' ) ),
				d.warWon ? h( 'button.btn.btn-primary', { onclick: () => ctx.act.closeDebrief( false ) }, t( 'common.confirm' ) ) : null,
			),
		);
	},

	away( ctx, o ) {
		const a = o.away;
		return modal( `away-modal tone-${ a.tone }`,
			h( 'div.paper-kicker', null, t( 'away.title' ) ),
			h( 'h2', null, t( `world.${ a.id }` ) ),
			h( 'p', null, t( 'away.body', { minutes: a.minutes } ) ),
			h( 'div.modal-actions', null,
				h( 'button.btn.btn-primary', { onclick: () => { ctx.act.overlay( null ); if ( a.node ) { ctx.set( { screen: 'war', tab: 'map' } ); ctx.act.select( a.node ); } } }, t( 'away.ok' ) ) ),
		);
	},

	final( ctx ) {
		const s = ctx.state;
		return modal( 'final-modal',
			logo( 'logo-final' ),
			stamp( t( 'final.title' ).toUpperCase(), 'stamp-win' ),
			h( 'p.final-body', null, t( 'final.body', { faction: s.faction } ) ),
			h( 'p.final-stats', null, t( 'final.stats', { battles: s.stats.battles, wins: s.stats.wins, captured: s.stats.captured, days: s.day } ) ),
			h( 'p.final-thanks', null, t( 'final.thanks' ) ),
			h( 'div.modal-actions', null,
				h( 'button.btn', { onclick: () => ctx.act.overlay( null ) }, t( 'common.close' ) ),
				h( 'button.btn.btn-primary', { onclick: ctx.act.reset }, t( 'final.again' ) ) ),
		);
	},

	error( ctx, o ) {
		return modal( 'error-modal',
			h( 'h2', null, t( 'coord.failed' ) ),
			h( 'p', null, o.message ),
			h( 'div.modal-actions', null, h( 'button.btn.btn-primary', { onclick: () => ctx.act.overlay( null ) }, t( 'common.close' ) ) ) );
	},

	//-------------------------------------------------------------------------
	settings( ctx, o ) {
		const s = ctx.state;
		const row = ( label, control ) => h( 'div.setting', null, h( 'span.setting-label', null, label ), control );

		return modal( 'settings-modal',
			h( 'header.modal-head', null, h( 'h2', null, t( 'settings.title' ) ), closeButton( () => ctx.act.overlay( null ) ) ),
			// The game decides the language; only a browser preview offers a choice.
			ctx.bridge === 'browser' && row( t( 'settings.language' ), h( 'div.segmented', null, LANGUAGES.map( ( [ id, label ] ) =>
				h( 'button', { class: language() === id ? 'active' : '', onclick: () => ctx.act.setLanguage( id ) }, label ) ) ) ),
			row( t( 'settings.sound' ), h( 'div.segmented', null,
				h( 'button', { class: s.prefs.sound ? 'active' : '', onclick: () => !s.prefs.sound && ctx.act.toggleSound() }, t( 'settings.on' ) ),
				h( 'button', { class: !s.prefs.sound ? 'active' : '', onclick: () => s.prefs.sound && ctx.act.toggleSound() }, t( 'settings.off' ) ) ) ),
			ctx.bridge === 'game' ? row( t( 'title.options' ), h( 'button.btn.btn-small', { onclick: ctx.act.options }, t( 'settings.options' ) ) ) : null,
			s.faction ? h( 'div.danger', null,
				o.confirmReset
					? [ h( 'p', null, t( 'settings.resetConfirm' ) ),
						h( 'div.modal-actions', null,
							h( 'button.btn', { onclick: () => ctx.set( { overlay: { type: 'settings' } } ) }, t( 'common.cancel' ) ),
							h( 'button.btn.btn-bad', { onclick: ctx.act.reset }, t( 'settings.resetYes' ) ) ) ]
					: h( 'button.btn.btn-bad.btn-small', { onclick: () => ctx.set( { overlay: { type: 'settings', confirmReset: true } } ) }, t( 'settings.reset' ) ),
			) : null,
			ctx.ui.dev ? devTools( ctx ) : null,
			h( 'div.settings-foot', null, `${ t( 'app.name' ) } · ${ t( 'app.demo' ) } · ${ ctx.bridge }` ),
		);
	},
};

function devTools( ctx ) {
	const s = ctx.state;
	return h( 'div.dev', null,
		h( 'div.block-title', null, t( 'settings.dev' ) ),
		h( 'div.dev-row', null,
			[ 1, 2, 3 ].map( n => h( 'button.btn.btn-small', { disabled: !s.op, onclick: () => ctx.act.devStage( n ) }, `stage ${ n }` ) ),
			h( 'button.btn.btn-small', { disabled: !s.pending, onclick: () => ctx.act.devResolve( 'ally' ) }, 'force win' ),
			h( 'button.btn.btn-small', { disabled: !s.pending, onclick: () => ctx.act.devResolve( 'enemy' ) }, 'force loss' ),
		),
		h( 'pre.dev-state', null, JSON.stringify( { faction: s.faction, day: s.day, op: s.op, pending: s.pending, stats: s.stats, owner: factionOf( s, 'ally' ) }, null, 1 ) ),
	);
}
