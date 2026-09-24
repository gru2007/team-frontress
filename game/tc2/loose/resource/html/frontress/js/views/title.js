// The title screen: what the game opens on, instead of the stock main menu.

import { h } from '../core/dom.js';
import { t } from '../core/i18n.js';
import { controlShare } from '../game/campaign.js';
import { logo, emblem } from './parts.js';

const POSTERS = [ 'soldier', 'heavyweapons', 'engineer', 'medic', 'demoman' ];

export function renderTitle( ctx ) {
	const { state: s, act } = ctx;
	const started = !!s.faction;

	const status = started
		? t( 'title.status', { day: s.day, faction: s.faction, share: Math.round( controlShare( s ) * 100 ) } )
		: t( 'title.fresh' );

	return h( 'div.screen.title-screen', null,
		h( 'div.title-backdrop' ),
		h( 'div.title-posters', null,
			POSTERS.map( ( p, i ) => h( 'div', { class: `poster p${ i }`, style: { '--i': i } },
				h( 'img', { src: `../img/classes/posters/${ p }.png`, alt: '' } ) ) ) ),
		h( 'div.title-column', null,
			logo(),
			h( 'p.tagline', null, t( 'app.tagline' ) ),
			h( 'nav.title-menu', null,
				started
					? h( 'button.btn.btn-primary.btn-xl', { onclick: act.enter },
						s.faction ? emblem( s.faction, 'btn-emblem' ) : null, t( 'title.continue' ) )
					: h( 'button.btn.btn-primary.btn-xl', { onclick: act.enter }, t( 'title.new' ) ),
				h( 'button.btn', { onclick: () => act.overlay( 'settings' ) }, t( 'title.settings' ) ),
				h( 'button.btn', { onclick: act.options }, t( 'title.options' ) ),
				h( 'button.btn.btn-quiet', { onclick: act.quit }, t( 'title.quit' ) ),
			),
			h( 'div.title-status', null,
				h( 'span.dot', { class: started ? `dot ${ s.faction }` : 'dot' } ), status ),
			h( 'p.lore', null, t( 'title.lore' ) ),
		),
		h( 'div.demo-badge', null,
			h( 'strong', null, t( 'app.demo' ) ),
			h( 'span', null, t( 'app.offline' ) ) ),
	);
}
