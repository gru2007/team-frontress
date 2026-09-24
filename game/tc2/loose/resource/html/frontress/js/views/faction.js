// Choosing a side: shown once, on the first launch of a campaign.

import { h } from '../core/dom.js';
import { t } from '../core/i18n.js';
import { emblem } from './parts.js';

const SIDES = [
	{ id: 'RED', poster: 'soldier' },
	{ id: 'BLU', poster: 'engineer' },
];

export function renderFaction( ctx ) {
	const { act } = ctx;

	return h( 'div.screen.faction-screen', null,
		h( 'header.faction-head', null,
			h( 'h1', null, t( 'faction.heading' ) ),
			h( 'p', null, t( 'faction.sub' ) ) ),
		h( 'div.faction-split', null,
			SIDES.map( side => h( 'section', {
				class: `faction-side side-${ side.id } hoverable`,
				onclick: () => act.overlay( 'contract', { faction: side.id } ),
			},
				h( 'div.side-bg' ),
				h( 'div.side-poster', null, h( 'img', { src: `../img/classes/posters/${ side.poster }.png`, alt: '' } ) ),
				h( 'div.side-content', null,
					emblem( side.id, 'side-emblem' ),
					h( 'div.side-name', null, t( `faction.${ side.id }.name` ) ),
					h( 'div.side-full', null, t( `faction.${ side.id }.full` ) ),
					h( 'p.side-motto', null, `“${ t( `faction.${ side.id }.motto` ) }”` ),
					h( 'button.btn.btn-side', null, t( 'faction.pick', { faction: side.id } ) ),
				),
			) ),
			h( 'div.vs', null, h( 'span', null, 'VS' ) ),
		),
		h( 'footer.faction-foot', null, t( 'faction.same' ) ),
	);
}
