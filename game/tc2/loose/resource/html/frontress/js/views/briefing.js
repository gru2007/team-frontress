// "How the war works": the pitch, for someone who just installed the demo at
// a festival and has never heard of us. Every panel is one promise from the
// store page, shown rather than told where the demo can.

import { h } from '../core/dom.js';
import { t } from '../core/i18n.js';
import { stageTrack, chevrons, conditionChip, medal } from './parts.js';

// The coordinator's population table (README: "Designed for a small
// population first").
function scale( players ) {
	const fronts = players < 16 ? 1 : players < 32 ? 2 : players < 48 ? 3 : 4;
	const per = players / fronts;
	const size = per < 8 ? '3v3' : per < 12 ? '4v4' : per < 18 ? '6v6' : per < 24 ? '9v9' : '12v12';
	return { fronts, size };
}

export function renderBriefing( ctx ) {
	const players = ctx.ui.briefPlayers ?? 12;
	const sc = scale( players );

	const slider = h( 'input.range', {
		type: 'range', min: 2, max: 64, value: players,
		oninput: e => {
			ctx.ui.briefPlayers = +e.target.value;
			const r = scale( +e.target.value );
			const root = e.target.closest( '.scale-demo' );
			root.querySelector( '.v-players' ).textContent = e.target.value;
			root.querySelector( '.v-fronts' ).textContent = r.fronts;
			root.querySelector( '.v-size' ).textContent = r.size;
			root.querySelectorAll( '.front-pip' ).forEach( ( p, i ) => p.classList.toggle( 'on', i < r.fronts ) );
		},
	} );

	return h( 'div.page.briefing-page', { 'data-scroll': 'briefing' },
		h( 'header.page-head', null,
			h( 'h1', null, t( 'brief.title' ) ),
			h( 'p.big', null, t( 'brief.sub' ) ) ),

		h( 'div.brief-grid', null,
			h( 'section.card.brief-card.loop-card', null,
				h( 'h2', null, t( 'brief.loop.title' ) ),
				h( 'div.loop', null,
					h( 'div.loop-node', null, t( 'brief.loop.a' ) ),
					h( 'div.loop-arrow' ),
					h( 'div.loop-node.hot', null, t( 'brief.loop.b' ) ),
					h( 'div.loop-arrow' ),
					h( 'div.loop-node', null, t( 'brief.loop.c' ) ),
					h( 'div.loop-return' ),
				),
				h( 'p', null, t( 'brief.loop.body' ) ),
			),

			h( 'section.card.brief-card', null,
				h( 'h2', null, t( 'brief.op.title' ) ),
				stageTrack( 2, { modes: [ 'ctf', 'pl', 'ad' ] } ),
				h( 'p', null, t( 'brief.op.body' ) ),
			),

			h( 'section.card.brief-card', null,
				h( 'h2', null, t( 'brief.deploy.title' ) ),
				h( 'div.mini-deploy', null, chevrons(), 'DEPLOY' ),
				h( 'p', null, t( 'brief.deploy.body' ) ),
			),

			h( 'section.card.brief-card.scale-demo', null,
				h( 'h2', null, t( 'brief.scale.title' ) ),
				h( 'div.scale-readout', null,
					h( 'div', null, h( 'strong.v-players', null, String( players ) ), h( 'span', null, t( 'brief.scale.players' ) ) ),
					h( 'div', null, h( 'strong.v-fronts', null, String( sc.fronts ) ), h( 'span', null, t( 'brief.scale.fronts' ) ) ),
					h( 'div', null, h( 'strong.v-size', null, sc.size ), h( 'span', null, t( 'brief.scale.size' ) ) ),
				),
				slider,
				h( 'div.front-pips', null, [ 0, 1, 2, 3 ].map( i => h( 'span', { class: `front-pip ${ i < sc.fronts ? 'on' : '' }` } ) ) ),
				h( 'p', null, t( 'brief.scale.body' ) ),
			),

			h( 'section.card.brief-card', null,
				h( 'h2', null, t( 'brief.cond.title' ) ),
				h( 'div.cond-list', null, [ 'snipers', 'sentries', 'lowgrav' ].map( id => conditionChip( id ) ) ),
				h( 'p', null, t( 'brief.cond.body' ) ),
			),

			h( 'section.card.brief-card', null,
				h( 'h2', null, t( 'brief.you.title' ) ),
				h( 'div.medal-row', null, [ 'mvp', 'slayer', 'lifeline' ].map( medal ) ),
				h( 'p', null, t( 'brief.you.body' ) ),
			),

			h( 'section.card.brief-card.wide', null,
				h( 'h2', null, t( 'brief.persist.title' ) ),
				h( 'p', null, t( 'brief.persist.body' ) ),
			),
		),
		h( 'p.demo-note', null, t( 'brief.demoNote' ) ),
	);
}
